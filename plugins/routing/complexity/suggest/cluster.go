package suggest

import (
	"math"
	"math/rand"
	"slices"
	"sort"
	"sync/atomic"
)

// How requests become clusters, at any volume.
//
// A run can see more requests than it should hold at once, and k-means has to
// hold what it clusters. So the Clusterer takes requests a batch at a time,
// over-clusters each batch with spherical k-means, and keeps of every cluster
// only what the rest of the run needs: the weighted sum of its members'
// vectors, its request count, and its members closest to the centre. That is
// enough to merge clusters from different batches as if they had come from
// one pass: a merged centre is the sum of the sums, and cohesion is the length
// of the sum over the summed weight.
//
// After the first batch, a request close enough to a cluster already found
// joins it instead of being clustered again, and only the rest wait in a pool
// that is clustered once it fills a batch. Without that, a request type with a
// few members in each batch is never dense enough within one batch to form
// its own cluster, and k-means pairs it with whatever is nearest.
//
// Memory: the pool holds fewer than BatchSize vectors, and the kept clusters
// hold at most MaxMembers vectors each and never more than the requests
// added, so a run holds min(requests, MaxClusters*MaxMembers) member vectors
// plus one pool; at 1536 dimensions a vector is 6 KB.
//
// Over-clustering is deliberate. Two request types sharing a cluster produce
// one phrase that fits neither, which nothing downstream can undo; one type
// split across clusters is joined again by the merge.

// ClusterOptions tunes clustering. Zero fields take the defaults below.
type ClusterOptions struct {
	AvgSize         int     // k per batch = batch size / AvgSize
	MergeSimilarity float64 // join clusters whose centres are at least this similar
	JoinSimilarity  float64 // a request this similar to a cluster centre joins it
	MinRequests     int     // drop finished clusters carrying fewer requests
	BatchSize       int     // requests clustered together by k-means
	MaxMembers      int     // members kept per cluster, closest to the centre
	MaxClusters     int     // clusters held between batches before compacting
	Iterations      int     // k-means iterations per batch
	Seed            int64
}

func (o ClusterOptions) withDefaults() ClusterOptions {
	if o.AvgSize <= 0 {
		o.AvgSize = 4
	}
	if o.MergeSimilarity <= 0 {
		o.MergeSimilarity = 0.85
	}
	if o.JoinSimilarity <= 0 {
		o.JoinSimilarity = 0.80
	}
	if o.MinRequests <= 0 {
		o.MinRequests = 6
	}
	if o.BatchSize <= 0 {
		// Measured on 16k WildChat prompts: 2,000 left sparse request types
		// too thin per pool to cluster (median cohesion 0.74, 196 clusters the
		// LLM called incoherent); 8,000 matched a single 16k pass (121 vs 116)
		// in 1m40s instead of 4m08s.
		o.BatchSize = 8000
	}
	if o.MaxMembers <= 0 {
		o.MaxMembers = 64
	}
	if o.MaxClusters <= 0 {
		o.MaxClusters = 8000
	}
	if o.Iterations <= 0 {
		o.Iterations = 25
	}
	if o.Seed == 0 {
		o.Seed = 7
	}
	return o
}

// Member is one request kept in a cluster, with what routes it today.
type Member struct {
	Request
	Similarity  float32 // to the cluster centre
	CurrentTier string  // tier of its nearest live phrase
	CurrentSim  float32 // similarity to that phrase
}

// Cluster is a group of requests the router's embedding space puts together.
type Cluster struct {
	Weight   int       // requests (Counts summed)
	Size     int       // distinct texts
	Centroid []float32 // unit length
	Cohesion float64   // weighted mean similarity of members to the centre
	Members  []Member  // closest to the centre first, at most MaxMembers

	sum     []float32 // sum of sqrt(Count)-weighted member vectors
	sumW    float64   // sum of sqrt(Count)
	current map[string]int
}

// CurrentRouting returns, over every request in the cluster, how many requests
// route to each tier today; the empty tier counts those below the match floor.
func (c *Cluster) CurrentRouting() map[string]int { return c.current }

// Clusterer accumulates requests into clusters batch by batch.
type Clusterer struct {
	opts   ClusterOptions
	live   []Phrase
	floor  float32
	rng    *rand.Rand
	groups []*Cluster
	pool   []Member // requests that joined no cluster, waiting for a batch
	sealed bool     // Assign has run: no new clusters are formed
}

// NewClusterer returns a clusterer that records, for each request, the live
// phrase that routes it today. live must carry vectors.
func NewClusterer(opts ClusterOptions, live []Phrase, floor float32) *Clusterer {
	opts = opts.withDefaults()
	return &Clusterer{opts: opts, live: live, floor: floor, rng: rand.New(rand.NewSource(opts.Seed))}
}

// Add takes requests in. Requests without a vector are skipped. Beyond the
// clusters already kept it holds at most BatchSize requests' vectors.
func (c *Clusterer) Add(reqs []Request) {
	for start := 0; start < len(reqs); start += c.opts.BatchSize {
		members := c.prepare(reqs[start:min(start+c.opts.BatchSize, len(reqs))])
		c.pool = append(c.pool, c.join(members)...)
		if len(c.pool) >= c.opts.BatchSize {
			c.clusterPool()
		}
	}
}

// prepare turns requests into members and records what routes each today.
func (c *Clusterer) prepare(batch []Request) []Member {
	members := make([]Member, 0, len(batch))
	for _, r := range batch {
		if len(r.Vector) == 0 {
			continue
		}
		r.Count = max(r.Count, 1)
		members = append(members, Member{Request: r})
	}
	parallelRange(len(members), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			if p, sim := nearest(members[i].Vector, c.live); p >= 0 {
				members[i].CurrentTier, members[i].CurrentSim = c.live[p].Tier, sim
			}
		}
	})
	return members
}

// join adds each member to the nearest kept cluster when it is at least
// JoinSimilarity to its centre, and returns the members that joined none.
func (c *Clusterer) join(members []Member) []Member {
	if len(c.groups) == 0 || len(members) == 0 {
		return members
	}
	target := make([]int, len(members))
	t := float32(c.opts.JoinSimilarity)
	parallelRange(len(members), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			best, bestSim := -1, t
			for g, cl := range c.groups {
				if s := dot(members[i].Vector, cl.Centroid); s >= bestSim {
					best, bestSim = g, s
				}
			}
			target[i] = best
		}
	})
	var rest []Member
	touched := map[int]bool{}
	for i, m := range members {
		g := target[i]
		if g < 0 {
			rest = append(rest, m)
			continue
		}
		c.groups[g].add(m, c.floor)
		touched[g] = true
	}
	for g := range touched {
		c.groups[g].refresh(c.opts.MaxMembers)
	}
	return rest
}

// clusterPool over-clusters the pooled members into new clusters and empties
// the pool, compacting the kept clusters if there are too many.
func (c *Clusterer) clusterPool() {
	ms := c.pool
	c.pool = nil
	if len(ms) == 0 {
		return
	}
	k := max(1, min(len(ms), int(math.Ceil(float64(len(ms))/float64(c.opts.AvgSize)))))
	assign := kmeans(ms, k, c.opts.Iterations, c.rng)
	byCentroid := make([][]Member, k)
	for i, a := range assign {
		byCentroid[a] = append(byCentroid[a], ms[i])
	}
	for _, g := range byCentroid {
		if len(g) > 0 {
			c.groups = append(c.groups, c.newCluster(g))
		}
	}
	if len(c.groups) > c.opts.MaxClusters {
		c.groups = mergeClusters(c.groups, c.opts.MergeSimilarity, c.opts.MaxMembers)
		if len(c.groups) > c.opts.MaxClusters {
			// Still over: shed the lightest, which are the least likely to
			// grow past MinRequests.
			sort.SliceStable(c.groups, func(i, j int) bool { return c.groups[i].Weight > c.groups[j].Weight })
			c.groups = c.groups[:c.opts.MaxClusters]
		}
	}
}

func (c *Clusterer) newCluster(ms []Member) *Cluster {
	cl := &Cluster{sum: make([]float32, len(ms[0].Vector)), current: map[string]int{}}
	for _, m := range ms {
		cl.add(m, c.floor)
	}
	cl.refresh(c.opts.MaxMembers)
	return cl
}

// add counts one member into the cluster's sums; refresh must follow.
func (cl *Cluster) add(m Member, floor float32) {
	w := float32(math.Sqrt(float64(m.Count)))
	for d, x := range m.Vector {
		cl.sum[d] += w * x
	}
	cl.sumW += float64(w)
	cl.Weight += m.Count
	cl.Size++
	tier := m.CurrentTier
	if m.CurrentSim < floor {
		tier = ""
	}
	cl.current[tier] += m.Count
	cl.Members = append(cl.Members, m)
}

// refresh recomputes the centre and cohesion from the sums and keeps the
// members closest to the centre.
func (cl *Cluster) refresh(maxMembers int) {
	cl.Centroid = normalize(cl.sum)
	if cl.sumW > 0 {
		cl.Cohesion = norm(cl.sum) / cl.sumW
	}
	for i := range cl.Members {
		cl.Members[i].Similarity = dot(cl.Members[i].Vector, cl.Centroid)
	}
	sort.SliceStable(cl.Members, func(i, j int) bool { return cl.Members[i].Similarity > cl.Members[j].Similarity })
	if len(cl.Members) > maxMembers {
		cl.Members = slices.Clone(cl.Members[:maxMembers])
	}
}

// Assign counts requests into the clusters already found without forming new
// ones, for traffic beyond what a run can afford to cluster: each request
// joins its nearest cluster when it is at least JoinSimilarity to the centre,
// exactly as a request does between batches, and the rest are reported as
// unmatched rather than pooled. The first call clusters what is still pooled
// and merges; Add must not be called after it.
//
// Assignment costs one dot product per cluster per request and keeps no
// vector beyond a cluster's MaxMembers, so it is O(requests * clusters) time
// and O(1) extra memory per request.
func (c *Clusterer) Assign(reqs []Request) (unmatched int) {
	if !c.sealed {
		c.clusterPool()
		c.groups = mergeClusters(c.groups, c.opts.MergeSimilarity, c.opts.MaxMembers)
		c.sealed = true
	}
	for start := 0; start < len(reqs); start += c.opts.BatchSize {
		members := c.prepare(reqs[start:min(start+c.opts.BatchSize, len(reqs))])
		for _, m := range c.join(members) {
			unmatched += m.Count
		}
	}
	return unmatched
}

// Clusters merges what was added and returns the clusters carrying at least
// MinRequests requests and two distinct texts, heaviest first.
func (c *Clusterer) Clusters() []*Cluster {
	c.clusterPool()
	c.groups = mergeClusters(c.groups, c.opts.MergeSimilarity, c.opts.MaxMembers)
	var out []*Cluster
	for _, cl := range c.groups {
		if cl.Weight >= c.opts.MinRequests && cl.Size >= 2 {
			out = append(out, cl)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	return out
}

// kmeans runs spherical k-means with k-means++ seeding and returns each
// member's cluster index.
func kmeans(ms []Member, k, iterations int, rng *rand.Rand) []int {
	centroids := seedCentroids(ms, k, rng)
	assign := make([]int, len(ms))
	for i := range assign {
		assign[i] = -1
	}
	for range iterations {
		var changed atomic.Int64
		parallelRange(len(ms), func(lo, hi int) {
			for i := lo; i < hi; i++ {
				best, bestSim := 0, float32(-2)
				for ci, cen := range centroids {
					if s := dot(ms[i].Vector, cen); s > bestSim {
						best, bestSim = ci, s
					}
				}
				if assign[i] != best {
					assign[i] = best
					changed.Add(1)
				}
			}
		})
		if changed.Load() == 0 {
			break
		}
		centroids = recomputeCentroids(ms, assign, centroids)
	}
	return assign
}

// seedCentroids is k-means++: each next seed is drawn with probability
// proportional to its squared distance from the nearest seed so far. Each
// point's best similarity is updated against the newest seed only, so seeding
// is O(n·k), not O(n·k²).
func seedCentroids(ms []Member, k int, rng *rand.Rand) [][]float32 {
	first := ms[rng.Intn(len(ms))].Vector
	centroids := [][]float32{first}
	best := make([]float32, len(ms))
	dist := make([]float64, len(ms))
	for i := range ms {
		best[i] = dot(ms[i].Vector, first)
	}
	for len(centroids) < k {
		var total float64
		for i := range ms {
			d := float64(1 - best[i])
			dist[i] = d * d
			total += dist[i]
		}
		if total <= 0 {
			break
		}
		r := rng.Float64() * total
		pick := len(ms) - 1
		for i := range ms {
			if r -= dist[i]; r <= 0 {
				pick = i
				break
			}
		}
		seed := ms[pick].Vector
		centroids = append(centroids, seed)
		parallelRange(len(ms), func(lo, hi int) {
			for i := lo; i < hi; i++ {
				best[i] = max(best[i], dot(ms[i].Vector, seed))
			}
		})
	}
	return centroids
}

func recomputeCentroids(ms []Member, assign []int, old [][]float32) [][]float32 {
	dim := len(ms[0].Vector)
	sums := make([][]float32, len(old))
	seen := make([]bool, len(old))
	for c := range sums {
		sums[c] = make([]float32, dim)
	}
	for i, m := range ms {
		c := assign[i]
		seen[c] = true
		// sqrt damps exact duplicates so one hot text doesn't own a centre.
		w := float32(math.Sqrt(float64(m.Count)))
		for d, x := range m.Vector {
			sums[c][d] += w * x
		}
	}
	for c := range sums {
		if seen[c] {
			sums[c] = normalize(sums[c])
		} else {
			sums[c] = old[c]
		}
	}
	return sums
}

// mergeClusters joins clusters whose centres are at least threshold similar,
// in rounds: each round every cluster finds its most similar partner, and
// pairs that chose each other merge. The most similar remaining pair always
// chose each other, so every round with a pair above threshold makes progress,
// and memory stays O(clusters) - no pairwise matrix.
func mergeClusters(groups []*Cluster, threshold float64, maxMembers int) []*Cluster {
	t := float32(threshold)
	for len(groups) > 1 {
		partner := make([]int, len(groups))
		parallelRange(len(groups), func(lo, hi int) {
			for i := lo; i < hi; i++ {
				best, bestSim := -1, t
				for j := range groups {
					if j == i {
						continue
					}
					if s := dot(groups[i].Centroid, groups[j].Centroid); s >= bestSim {
						best, bestSim = j, s
					}
				}
				partner[i] = best
			}
		})
		merged := false
		absorbed := make([]bool, len(groups))
		for i, j := range partner {
			if j > i && partner[j] == i {
				groups[i].absorb(groups[j], maxMembers)
				absorbed[j] = true
				merged = true
			}
		}
		if !merged {
			break
		}
		kept := groups[:0]
		for i, g := range groups {
			if !absorbed[i] {
				kept = append(kept, g)
			}
		}
		groups = kept
	}
	return groups
}

func (cl *Cluster) absorb(o *Cluster, maxMembers int) {
	for d, x := range o.sum {
		cl.sum[d] += x
	}
	cl.sumW += o.sumW
	cl.Weight += o.Weight
	cl.Size += o.Size
	for k, v := range o.current {
		cl.current[k] += v
	}
	cl.Members = append(cl.Members, o.Members...)
	cl.refresh(maxMembers)
}

// representatives picks the medoid plus a spread of members from the
// cluster's core, farthest-first, so the LLM sees the variation inside the
// cluster instead of n near-copies of one request.
func representatives(cl *Cluster, n int) []Member {
	if len(cl.Members) == 0 {
		return nil
	}
	core := cl.Members[:max(1, int(math.Ceil(float64(len(cl.Members))*0.75)))]
	picked := []Member{core[0]}
	closest := make([]float32, len(core))
	for i := range core {
		closest[i] = dot(core[i].Vector, core[0].Vector)
	}
	for len(picked) < min(n, len(core)) {
		best, bestSim := -1, float32(2)
		for i := range core {
			if closest[i] < bestSim {
				best, bestSim = i, closest[i]
			}
		}
		if best < 0 || bestSim >= 0.999 {
			break
		}
		picked = append(picked, core[best])
		for i := range core {
			closest[i] = max(closest[i], dot(core[i].Vector, core[best].Vector))
		}
	}
	return picked
}

// nearest returns the index of the most similar phrase and its similarity,
// or -1 when there are none.
func nearest(vec []float32, set []Phrase) (int, float32) {
	best, bestSim := -1, float32(-2)
	for i := range set {
		if s := dot(vec, set[i].Vector); s > bestSim {
			best, bestSim = i, s
		}
	}
	return best, bestSim
}
