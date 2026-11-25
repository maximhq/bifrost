package suggest

import (
	"fmt"
	"math/rand"
	"testing"
)

const testDim = 32

// anchor returns the unit basis vector e_i.
func anchor(i int) []float32 {
	v := make([]float32, testDim)
	v[i] = 1
	return v
}

// near returns a unit vector close to e_i (cosine ~0.95+).
func near(i int, rng *rand.Rand) []float32 {
	v := anchor(i)
	for d := range v {
		v[d] += float32(rng.NormFloat64()) * 0.04
	}
	return normalize(v)
}

// typeRequests builds n distinct requests around anchor i, IDs prefixed by name.
func typeRequests(name string, i, n int, rng *rand.Rand) []Request {
	out := make([]Request, n)
	for j := range out {
		out[j] = Request{ID: fmt.Sprintf("%s-%d", name, j), Text: fmt.Sprintf("%s request %d", name, j), Count: 1, Vector: near(i, rng)}
	}
	return out
}

func shuffled(rng *rand.Rand, groups ...[]Request) []Request {
	var all []Request
	for _, g := range groups {
		all = append(all, g...)
	}
	rng.Shuffle(len(all), func(a, b int) { all[a], all[b] = all[b], all[a] })
	return all
}

func TestClustererJoinsTypesSplitAcrossBatches(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	reqs := shuffled(rng, typeRequests("a", 0, 30, rng), typeRequests("b", 1, 30, rng), typeRequests("c", 2, 30, rng))
	c := NewClusterer(ClusterOptions{BatchSize: 25, AvgSize: 4, MinRequests: 6}, nil, 0.4)
	c.Add(reqs)
	clusters := c.Clusters()
	if len(clusters) != 3 {
		t.Fatalf("got %d clusters, want 3", len(clusters))
	}
	for _, cl := range clusters {
		if cl.Weight != 30 || cl.Size != 30 {
			t.Errorf("cluster weight %d size %d, want 30/30", cl.Weight, cl.Size)
		}
		first := cl.Members[0].ID[:1]
		for _, m := range cl.Members {
			if m.ID[:1] != first {
				t.Errorf("cluster mixes %s and %s", first, m.ID)
			}
		}
		if cl.Cohesion < 0.9 {
			t.Errorf("cohesion %.3f, want >= 0.9 for a tight type", cl.Cohesion)
		}
	}
}

func TestClustererJoinsLaterRequestsToFoundClusters(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	c := NewClusterer(ClusterOptions{BatchSize: 10, MinRequests: 1}, nil, 0.4)
	c.Add(typeRequests("a", 0, 10, rng))
	found := len(c.groups)
	// More of the same type join what was found instead of waiting in the
	// pool; a new type waits there until Clusters.
	c.Add(typeRequests("x", 0, 5, rng))
	c.Add(typeRequests("b", 1, 4, rng))
	if len(c.groups) != found {
		t.Fatalf("clusters %d -> %d, want later requests joined, not re-clustered", found, len(c.groups))
	}
	if len(c.pool) != 4 {
		t.Fatalf("pool holds %d, want only the 4 requests of the new type", len(c.pool))
	}
	got := c.Clusters()
	if len(c.pool) != 0 {
		t.Fatalf("Clusters left %d requests in the pool", len(c.pool))
	}
	var weights []int
	for _, cl := range got {
		weights = append(weights, cl.Weight)
	}
	if len(got) != 2 || got[0].Weight != 15 || got[1].Weight != 4 {
		t.Fatalf("cluster weights %v, want [15 4]", weights)
	}
}

func TestClustererDropsClustersBelowMinRequests(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	small := typeRequests("s", 3, 3, rng)
	c := NewClusterer(ClusterOptions{MinRequests: 6}, nil, 0.4)
	c.Add(shuffled(rng, typeRequests("a", 0, 20, rng), small))
	for _, cl := range c.Clusters() {
		if cl.Members[0].ID[:1] == "s" {
			t.Fatalf("a 3-request cluster survived MinRequests 6")
		}
	}
	// Counts add up: three texts seen twice each clear the bar.
	for i := range small {
		small[i].Count = 2
	}
	c = NewClusterer(ClusterOptions{MinRequests: 6}, nil, 0.4)
	c.Add(small)
	if got := c.Clusters(); len(got) != 1 || got[0].Weight != 6 || got[0].Size != 3 {
		t.Fatalf("counts not summed into weight: %+v", got)
	}
}

func TestClustererBoundsMembersButKeepsExactTotals(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	c := NewClusterer(ClusterOptions{MaxMembers: 5, BatchSize: 10}, nil, 0.4)
	c.Add(typeRequests("a", 0, 40, rng))
	clusters := c.Clusters()
	if len(clusters) != 1 {
		t.Fatalf("got %d clusters, want 1", len(clusters))
	}
	cl := clusters[0]
	if len(cl.Members) != 5 {
		t.Errorf("kept %d members, want 5", len(cl.Members))
	}
	if cl.Weight != 40 || cl.Size != 40 {
		t.Errorf("weight %d size %d, want exact 40/40", cl.Weight, cl.Size)
	}
	for i := 1; i < len(cl.Members); i++ {
		if cl.Members[i].Similarity > cl.Members[i-1].Similarity {
			t.Fatalf("members not sorted closest first")
		}
	}
}

func TestClustererRecordsCurrentRouting(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	live := []Phrase{{Tier: "SIMPLE", Text: "a phrase", Vector: anchor(0)}}
	c := NewClusterer(ClusterOptions{}, live, 0.4)
	c.Add(shuffled(rng, typeRequests("a", 0, 10, rng), typeRequests("b", 1, 10, rng)))
	for _, cl := range c.Clusters() {
		cur := cl.CurrentRouting()
		switch cl.Members[0].ID[:1] {
		case "a":
			if cur["SIMPLE"] != 10 {
				t.Errorf("type a routes %v, want SIMPLE 10", cur)
			}
		case "b":
			if cur[""] != 10 {
				t.Errorf("type b routes %v, want all below floor", cur)
			}
		}
	}
}

func TestClustererSkipsRequestsWithoutVectors(t *testing.T) {
	c := NewClusterer(ClusterOptions{MinRequests: 1}, nil, 0.4)
	c.Add([]Request{{ID: "x", Text: "no vector"}})
	if got := c.Clusters(); len(got) != 0 {
		t.Fatalf("clustered a request without a vector: %+v", got)
	}
}

func TestMergeClustersOnlyJoinsSimilarCentres(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	c := NewClusterer(ClusterOptions{}, nil, 0.4)
	mk := func(name string, i int) *Cluster {
		ms := make([]Member, 4)
		for j := range ms {
			ms[j] = Member{Request: Request{ID: name, Count: 1, Vector: near(i, rng)}}
		}
		return c.newCluster(ms)
	}
	got := mergeClusters([]*Cluster{mk("a1", 0), mk("b", 1), mk("a2", 0)}, 0.85, 64)
	if len(got) != 2 {
		t.Fatalf("got %d clusters, want a1+a2 merged and b alone", len(got))
	}
	for _, g := range got {
		if g.Members[0].ID != "b" && g.Weight != 8 {
			t.Errorf("merged weight %d, want 8", g.Weight)
		}
	}
}

func TestRepresentativesStartAtMedoidAndSpread(t *testing.T) {
	rng := rand.New(rand.NewSource(6))
	c := NewClusterer(ClusterOptions{}, nil, 0.4)
	c.Add(typeRequests("a", 0, 20, rng))
	cl := c.Clusters()[0]
	reps := representatives(cl, 5)
	if len(reps) != 5 {
		t.Fatalf("got %d representatives, want 5", len(reps))
	}
	if reps[0].ID != cl.Members[0].ID {
		t.Errorf("first representative %s, want medoid %s", reps[0].ID, cl.Members[0].ID)
	}
	seen := map[string]bool{}
	for _, r := range reps {
		if seen[r.ID] {
			t.Fatalf("representative %s picked twice", r.ID)
		}
		seen[r.ID] = true
	}
}

// Requests past the clustering budget are counted into the clusters found,
// with their routing, and requests of an unseen type are reported unmatched
// instead of forming a cluster.
func TestClustererAssignCountsWithoutFormingClusters(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	live := []Phrase{{Tier: "SIMPLE", Text: "a phrase", Vector: anchor(0)}}
	c := NewClusterer(ClusterOptions{BatchSize: 50, MinRequests: 10}, live, 0.4)
	c.Add(typeRequests("a", 0, 8, rng))
	unmatched := c.Assign(append(typeRequests("more-a", 0, 5, rng), typeRequests("new", 5, 4, rng)...))
	if unmatched != 4 {
		t.Fatalf("unmatched %d, want the 4 requests of the unseen type", unmatched)
	}
	if c.Assign(nil) != 0 {
		t.Fatal("assigning nothing matches nothing")
	}
	got := c.Clusters()
	if len(got) != 1 || got[0].Weight != 13 {
		t.Fatalf("want one cluster of 13 (8 clustered + 5 assigned, clearing MinRequests 10), got %d clusters", len(got))
	}
	if got[0].CurrentRouting()["SIMPLE"] != 13 {
		t.Errorf("assigned requests count toward today's routing: %v", got[0].CurrentRouting())
	}
}

// A cluster holding two request types that k-means put together comes apart
// when both halves are big enough and their centres are further apart than
// the join threshold; one type, or a half too small to suggest for, stays.
func TestClustererSplitsTwoTypesInOneCluster(t *testing.T) {
	rng := rand.New(rand.NewSource(51))
	c := NewClusterer(ClusterOptions{MinRequests: 6}, nil, 0.4)
	mixed := c.newCluster(c.prepare(append(typeRequests("a", 0, 8, rng), typeRequests("b", 1, 7, rng)...)))
	got := c.splitClusters([]*Cluster{mixed})
	if len(got) != 2 || got[0].Weight+got[1].Weight != 15 {
		t.Fatalf("want the two types apart, got %d clusters", len(got))
	}
	for _, cl := range got {
		first := cl.Members[0].ID[:1]
		for _, m := range cl.Members {
			if m.ID[:1] != first {
				t.Fatalf("a split half mixes %s and %s", first, m.ID)
			}
		}
	}

	one := c.newCluster(c.prepare(typeRequests("a", 0, 15, rng)))
	if got := c.splitClusters([]*Cluster{one}); len(got) != 1 {
		t.Errorf("one type split into %d", len(got))
	}
	lopsided := c.newCluster(c.prepare(append(typeRequests("a", 0, 12, rng), typeRequests("b", 1, 3, rng)...)))
	if got := c.splitClusters([]*Cluster{lopsided}); len(got) != 1 {
		t.Errorf("a half under MinRequests should stay, got %d clusters", len(got))
	}
	off := NewClusterer(ClusterOptions{MinRequests: 6, SplitSimilarity: -1}, nil, 0.4)
	if got := off.splitClusters([]*Cluster{mixed}); len(got) != 1 {
		t.Errorf("a negative SplitSimilarity disables splitting")
	}
}

// Fragments of one small type, each too small to keep, are merged into a
// cluster of their own instead of being dropped; a fragment is never merged
// into a cluster already big enough to keep.
func TestClustererRescuesFragmentsOfSmallTypes(t *testing.T) {
	rng := rand.New(rand.NewSource(52))
	c := NewClusterer(ClusterOptions{MinRequests: 6}, nil, 0.4)
	small := typeRequests("s", 3, 9, rng)
	frags := []*Cluster{
		c.newCluster(c.prepare(small[:3])),
		c.newCluster(c.prepare(small[3:6])),
		c.newCluster(c.prepare(small[6:])),
	}
	big := c.newCluster(c.prepare(typeRequests("b", 3, 10, rng)))
	got := c.rescueSmall(append([]*Cluster{big}, frags...))
	if len(got) != 2 {
		t.Fatalf("want the big cluster and one rescued cluster, got %d", len(got))
	}
	if got[0] != big || big.Weight != 10 {
		t.Fatalf("the big cluster must be left alone (weight %d)", big.Weight)
	}
	if got[1].Weight != 9 {
		t.Fatalf("rescued cluster weight %d, want the 9 fragment requests", got[1].Weight)
	}
	off := NewClusterer(ClusterOptions{MinRequests: 6, RescueSimilarity: -1}, nil, 0.4)
	if got := off.rescueSmall(append([]*Cluster{big}, frags...)); len(got) != 4 {
		t.Errorf("a negative RescueSimilarity disables rescue, got %d", len(got))
	}
}
