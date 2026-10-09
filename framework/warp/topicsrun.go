package warp

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/bits"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// One topics run, at any size.
//
// A window can hold more requests than fit in memory, and k-means has to hold
// what it clusters. So a run never holds the window: it reads it a slice at a
// time, clusters what it reads in batches, and keeps of each cluster only what
// the rest of the run needs - the sum of its members' vectors, how many there
// are, and the few nearest its centre. The batches' clusters are then merged
// as if they had come from one pass, which those three are enough for: a
// merged cluster's centre is the sum of the sums, and its cohesion is the
// length of that sum over the count.
//
// Past topicLimits.clustered requests the run stops clustering. What it has
// found are the topics, and every request after that is given to the nearest
// of them. That costs one page of vectors at a time, however long the window.

// topicLimits bounds one run.
type topicLimits struct {
	// batch is how many requests are clustered together, and so the most a
	// run holds at once.
	batch int
	// clustered is how many requests are clustered before the rest are
	// assigned to what that found.
	clustered int
	// slice is how many requests a slice of the window is meant to hold, and
	// sliceCeiling how many it may hold before it is split: the stores put a
	// ceiling on one filtered read, 10,000 by default on two of them, and a
	// slice has to stay well under it.
	slice        int
	sliceCeiling int
	page         int64
	// read is how many requests one run takes in, or zero for no bound. A
	// window that holds more is not read to the end.
	read int
}

// defaultTopicLimits is what a deployment runs with. One run takes in 20,000
// requests and clusters all of them: within that bound nothing is left over
// to assign, and a window that holds more is sampled across its length.
func defaultTopicLimits() topicLimits {
	return topicLimits{batch: topicsMaxVectors, clustered: topicsMaxVectors, read: topicsMaxVectors, slice: 2000, sliceCeiling: 5000, page: topicsPageSize}
}

// errTopicReadLimit stops a read that has taken in all a run may.
var errTopicReadLimit = errors.New("topic read limit reached")

// topicsFloorSample is how many clustered requests are kept, vectors and
// all, to find the floor below which a request matches no topic.
const topicsFloorSample = 2000

// topicsUnassignedPercentile is where that floor sits: a request less like
// its nearest topic than all but this share of the clustered ones were like
// theirs is not counted as that topic's.
const topicsUnassignedPercentile = 0.02

// topicSample is one of a cluster's nearest members, kept with its vector so
// it can be measured against the centre of whatever the cluster is merged into.
type topicSample struct {
	logID  string
	vector []float32
}

// topicCluster is a cluster as the run keeps it.
type topicCluster struct {
	// sum is the sum of the members' unit vectors, and size how many there
	// are. The centre and the cohesion both follow from the two.
	sum  []float32
	size int
	// samples are the members nearest the centre, nearest first.
	samples   []topicSample
	sampleIDs []string
	label     string
}

// centroid is the cluster's centre: the direction of the sum.
func (c *topicCluster) centroid() []float32 {
	return unitVector(c.sum)
}

// cohesion is the members' average similarity to the centre. For unit
// vectors that is the length of their sum over their number, so it is exact
// without the members.
func (c *topicCluster) cohesion() float64 {
	if c.size == 0 {
		return 0
	}
	var norm float64
	for _, value := range c.sum {
		norm += float64(value) * float64(value)
	}
	return math.Sqrt(norm) / float64(c.size)
}

// absorb folds another cluster into this one and picks the samples again,
// from both clusters', against the new centre.
func (c *topicCluster) absorb(other topicCluster) {
	for d, value := range other.sum {
		c.sum[d] += value
	}
	c.size += other.size
	c.setSamples(append(c.samples, other.samples...))
}

func (c *topicCluster) setSamples(candidates []topicSample) {
	centre := c.centroid()
	sort.SliceStable(candidates, func(i, j int) bool {
		return dot(candidates[i].vector, centre) > dot(candidates[j].vector, centre)
	})
	c.samples, c.sampleIDs = c.samples[:0:0], nil
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if _, repeat := seen[candidate.logID]; repeat {
			continue
		}
		seen[candidate.logID] = struct{}{}
		c.samples = append(c.samples, candidate)
		c.sampleIDs = append(c.sampleIDs, candidate.logID)
		if len(c.samples) == topicsLabelSamples {
			break
		}
	}
}

// summarizeClusters turns one k-means result into clusters the run can keep,
// and reports each member's similarity to its centre to observe.
func summarizeClusters(vectors []topicVector, assignments []int, centroids [][]float32, observe func(similarity float32)) []topicCluster {
	if len(vectors) == 0 || len(centroids) == 0 {
		return nil
	}
	dimension := len(vectors[0].vector)
	clusters := make([]topicCluster, len(centroids))
	members := make([][]topicSample, len(centroids))
	for index, cluster := range assignments {
		if cluster < 0 || cluster >= len(centroids) {
			continue
		}
		if clusters[cluster].sum == nil {
			clusters[cluster].sum = make([]float32, dimension)
		}
		for d, value := range vectors[index].vector {
			clusters[cluster].sum[d] += value
		}
		clusters[cluster].size++
		members[cluster] = append(members[cluster], topicSample{logID: vectors[index].logID, vector: vectors[index].vector})
	}
	kept := make([]topicCluster, 0, len(clusters))
	for cluster := range clusters {
		if clusters[cluster].size == 0 {
			// Its centroid is left over from seeding and stands for nothing.
			continue
		}
		if observe != nil {
			centre := clusters[cluster].centroid()
			for _, member := range members[cluster] {
				observe(dot(member.vector, centre))
			}
		}
		clusters[cluster].setSamples(members[cluster])
		kept = append(kept, clusters[cluster])
	}
	return kept
}

// topicMergeThreshold derives the similarity at which two clusters count as
// one subject: the average similarity of a request to the centre of its own
// cluster, across the run. Two centres that are as alike as that are no
// further apart than a topic's own members are from its middle.
//
// Clusters of one are left out. Each is perfectly similar to itself, which
// would drag the figure toward 1 on exactly the runs that are most over-split.
// A run with nothing but those has no threshold to derive.
func topicMergeThreshold(clusters []topicCluster) (float64, bool) {
	total, members := 0.0, 0
	for index := range clusters {
		if clusters[index].size < 2 {
			continue
		}
		total += clusters[index].cohesion() * float64(clusters[index].size)
		members += clusters[index].size
	}
	if members == 0 {
		return 0, false
	}
	return total / float64(members), true
}

// mergeTopicClusters joins clusters whose centres are at least threshold
// alike, and reports how many were folded into another.
//
// It joins the closest pair, works that pair's centre out again from its
// members, and measures again. Joining everything linked by a chain of close
// pairs would put A and C together because each is near B, however far apart
// they are themselves. The merged centre is the members' mean, so a cluster
// of three does not pull it as hard as a cluster of three hundred.
func mergeTopicClusters(clusters []topicCluster, threshold float64) ([]topicCluster, int) {
	k := len(clusters)
	if k < 2 {
		return clusters, 0
	}
	centres := make([][]float32, k)
	alive := make([]bool, k)
	for index := range clusters {
		centres[index] = clusters[index].centroid()
		alive[index] = clusters[index].size > 0
	}
	similarity := make([][]float32, k)
	for a := range similarity {
		similarity[a] = make([]float32, k)
		for b := a + 1; b < k; b++ {
			if alive[a] && alive[b] {
				similarity[a][b] = dot(centres[a], centres[b])
			}
		}
	}
	merged := 0
	for {
		bestA, bestB, best := -1, -1, float32(math.Inf(-1))
		for a := 0; a < k; a++ {
			if !alive[a] {
				continue
			}
			for b := a + 1; b < k; b++ {
				if alive[b] && similarity[a][b] > best {
					bestA, bestB, best = a, b, similarity[a][b]
				}
			}
		}
		if bestA < 0 || float64(best) < threshold {
			break
		}
		clusters[bestA].absorb(clusters[bestB])
		alive[bestB] = false
		centres[bestA] = clusters[bestA].centroid()
		merged++
		for other := 0; other < k; other++ {
			if other != bestA && alive[other] {
				similarity[min(bestA, other)][max(bestA, other)] = dot(centres[bestA], centres[other])
			}
		}
	}
	kept := make([]topicCluster, 0, k-merged)
	for index := range clusters {
		if alive[index] {
			kept = append(kept, clusters[index])
		}
	}
	return kept, merged
}

// joinTopicsByName joins clusters that were given the same name, and reports
// how many were folded into another.
//
// To anyone reading the list, two topics with one name are one topic. How
// alike their centres are does not settle it: clusters that share a name are
// found well below the similarity at which clusters that do not are, so the
// merge that runs on similarity cannot be tuned to catch them. The joined
// topic's centre, cohesion and examples are worked out again from both - a
// name shared by two different subjects then shows as a loose topic rather
// than hiding as two tight ones.
func joinTopicsByName(clusters []topicCluster) ([]topicCluster, int) {
	byName := map[string]int{}
	joined := make([]topicCluster, 0, len(clusters))
	folded := 0
	for _, cluster := range clusters {
		name := strings.ToLower(strings.Join(strings.Fields(cluster.label), " "))
		at, seen := byName[name]
		if !seen || name == "" {
			byName[name] = len(joined)
			// Its own sum, since the one it came with may be shared.
			cluster.sum = append([]float32(nil), cluster.sum...)
			joined = append(joined, cluster)
			continue
		}
		joined[at].absorb(cluster)
		folded++
	}
	if folded == 0 {
		return clusters, 0
	}
	sortTopicClusters(joined)
	return joined, folded
}

func sortTopicClusters(clusters []topicCluster) {
	sort.SliceStable(clusters, func(i, j int) bool { return clusters[i].size > clusters[j].size })
}

// topicRun is one run in progress.
type topicRun struct {
	limits topicLimits
	seed   int64

	// batch is what has been read and not yet clustered.
	batch []topicVector
	// clusters is every batch's clusters until the merge, and the topics after.
	clusters []topicCluster
	batches  int
	// centres are the topics' centres as the merge left them. Requests are
	// assigned against these and not against centres that move as requests
	// arrive, so which topic a request is given does not depend on when in
	// the run it was read.
	centres [][]float32
	merged  bool
	floor   float32
	// sample is a uniform sample of the clustered requests, kept to find the
	// floor, and rng what draws it.
	sample []topicVector
	rng    *rand.Rand

	// clustered requests went through k-means; dropped are the ones whose
	// cluster was too small to keep. assigned were given to a topic after the
	// merge, and unmatched were nearer no topic than the floor.
	clustered int
	dropped   int
	assigned  int
	unmatched int
	folded    int
	threshold *float64
}

func newTopicRun(limits topicLimits, seed int64) *topicRun {
	return &topicRun{limits: limits, seed: seed, rng: rand.New(rand.NewSource(seed))}
}

// add takes one request: into the batch while the run is still clustering,
// to its nearest topic once it is not.
func (r *topicRun) add(ctx context.Context, entry topicVector) error {
	if r.merged {
		r.assign(entry)
		return nil
	}
	r.batch = append(r.batch, entry)
	if len(r.batch) < r.limits.batch {
		return nil
	}
	if err := r.clusterBatch(ctx); err != nil {
		return err
	}
	if r.clustered >= r.limits.clustered {
		r.merge()
	}
	return nil
}

// finish clusters what is left and merges, if the run has not already.
func (r *topicRun) finish(ctx context.Context) error {
	if r.merged {
		return nil
	}
	if err := r.clusterBatch(ctx); err != nil {
		return err
	}
	r.merge()
	return nil
}

func (r *topicRun) clusterBatch(ctx context.Context) error {
	if len(r.batch) == 0 {
		return nil
	}
	points := make([][]float32, len(r.batch))
	for index := range r.batch {
		points[index] = r.batch[index].vector
	}
	assignments, centroids, err := kMeans(ctx, points, topicClusterCount(len(points)), topicsKMeansIterations, r.seed+int64(r.batches))
	if err != nil {
		return err
	}
	r.clusters = append(r.clusters, summarizeClusters(r.batch, assignments, centroids, nil)...)
	for _, entry := range r.batch {
		r.keepForFloor(entry)
	}
	r.batches++
	// Let go of the vectors: the clusters hold what is needed of them.
	r.batch = nil
	return nil
}

// keepForFloor counts one clustered request and keeps it with the chance
// that leaves every clustered request equally likely to be among those kept.
func (r *topicRun) keepForFloor(entry topicVector) {
	r.clustered++
	if len(r.sample) < topicsFloorSample {
		r.sample = append(r.sample, entry)
		return
	}
	if at := r.rng.Intn(r.clustered); at < topicsFloorSample {
		r.sample[at] = entry
	}
}

// merge joins the batches' clusters into the run's topics, settles what
// becomes of the small ones, and freezes the centres requests are assigned to.
func (r *topicRun) merge() {
	if threshold, ok := topicMergeThreshold(r.clusters); ok {
		r.clusters, r.folded = mergeTopicClusters(r.clusters, threshold)
		r.threshold = &threshold
	}
	r.floor = r.similarityFloor()
	r.settleSmallClusters()
	sortTopicClusters(r.clusters)
	r.centres = make([][]float32, len(r.clusters))
	for index := range r.clusters {
		r.centres[index] = r.clusters[index].centroid()
	}
	r.sample = nil
	r.merged = true
}

// smallClusterSize is the size under which a cluster is small, and the size
// under which it is too small to be a pattern at all.
//
// Small means a fragment: too few members to draw its examples from. Only a
// run of several batches has them to settle, since it is the batches that
// each leave a few behind. It is deliberately not a share of the run. Folding
// every cluster under one percent into its nearest topic took a deployment's
// ninety topics down to nine: most real topics are small beside the largest,
// and "like enough to be given to" is a far looser test than the merge's.
func (r *topicRun) smallClusterSize() (small, minSize int) {
	minSize = topicsMinClusterSize
	if r.clustered < topicsSmallCorpus {
		minSize = 1
	}
	if r.batches > 1 {
		return max(minSize, topicsLabelSamples), minSize
	}
	return minSize, minSize
}

// settleSmallClusters decides what becomes of the clusters the merge left
// small.
//
// Each batch leaves a few clusters of two or three, and a centre made from
// two or three members is mostly their noise: it sits further from its own
// subject's centre than the merge allows, so the fragment is left behind as a
// topic of two beside the topic of two thousand it belongs to. A fragment is
// treated as the requests in it would be if they arrived now. It joins the
// nearest topic it is like enough to be given to, and otherwise stands as a
// topic of its own - unless it is smaller than a pattern can be, when it is
// counted as matching none.
func (r *topicRun) settleSmallClusters() {
	small, minSize := r.smallClusterSize()
	topics := make([]topicCluster, 0, len(r.clusters))
	var leftover []topicCluster
	for _, cluster := range r.clusters {
		if cluster.size >= small {
			topics = append(topics, cluster)
		} else {
			leftover = append(leftover, cluster)
		}
	}
	centres := make([][]float32, len(topics))
	for index := range topics {
		centres[index] = topics[index].centroid()
	}
	sortTopicClusters(leftover)
	for _, cluster := range leftover {
		centre := cluster.centroid()
		best, bestScore := -1, float32(math.Inf(-1))
		for index := range centres {
			if score := dot(centre, centres[index]); score > bestScore {
				best, bestScore = index, score
			}
		}
		switch {
		case best >= 0 && bestScore >= r.floor:
			topics[best].absorb(cluster)
			r.folded++
		case cluster.size >= minSize:
			topics = append(topics, cluster)
		default:
			r.dropped += cluster.size
		}
	}
	r.clusters = topics
}

// similarityFloor is the similarity below which a request matches no topic:
// what all but the least alike few of the clustered requests have to the
// nearest topic. A request further from every topic than that is not one of
// theirs.
//
// It is measured against the topics as the merge left them, on a sample of
// the requests that made them. Measuring each request against the cluster
// k-means first put it in gives a figure that is too high to use: those
// clusters are small, a small cluster's centre is fitted to its few members,
// and nothing that arrives later is ever that close to a centre.
//
// For the same reason each sampled request is measured against its topic's
// centre with that request taken out of it. A centre leans toward every
// member that went into it, and a request that arrives later did not.
func (r *topicRun) similarityFloor() float32 {
	small, _ := r.smallClusterSize()
	var topics []*topicCluster
	var centres [][]float32
	for index := range r.clusters {
		if r.clusters[index].size >= small {
			topics = append(topics, &r.clusters[index])
			centres = append(centres, r.clusters[index].centroid())
		}
	}
	if len(centres) == 0 || len(r.sample) == 0 {
		return 0
	}
	nearest := make([]float32, len(r.sample))
	without := make([]float32, len(r.sample[0].vector))
	for index, entry := range r.sample {
		best, bestScore := 0, float32(math.Inf(-1))
		for topic, centre := range centres {
			if score := dot(entry.vector, centre); score > bestScore {
				best, bestScore = topic, score
			}
		}
		nearest[index] = bestScore
		if topics[best].size > 1 {
			for d, value := range topics[best].sum {
				without[d] = value - entry.vector[d]
			}
			nearest[index] = dot(entry.vector, unitVector(without))
		}
	}
	sort.Slice(nearest, func(i, j int) bool { return nearest[i] < nearest[j] })
	return nearest[int(float64(len(nearest))*topicsUnassignedPercentile)]
}

// assign gives a request to its nearest topic, or to none.
func (r *topicRun) assign(entry topicVector) {
	best, bestScore := -1, float32(math.Inf(-1))
	for index, centre := range r.centres {
		if score := dot(entry.vector, centre); score > bestScore {
			best, bestScore = index, score
		}
	}
	if best < 0 || bestScore < r.floor {
		r.unmatched++
		return
	}
	for d, value := range entry.vector {
		r.clusters[best].sum[d] += value
	}
	r.clusters[best].size++
	r.assigned++
}

// read is how many requests the run has taken in.
func (r *topicRun) read() int {
	return r.clustered + len(r.batch) + r.assigned + r.unmatched
}

// readTopicWindow hands every indexed request in the window to take, once,
// with its vector scaled to unit length.
//
// A store that filters on the server is read a slice of the window at a time.
// The slices are sized from how dense the window turns out to be and visited
// spread across it, not end to end, so that a run which stops clustering
// partway has clustered a sample of the whole window and not its first weeks.
// A store that does not filter on the server pays for the whole namespace on
// every filtered read, so it is read once, unfiltered, and the window applied
// here.
func (s *Service) readTopicWindow(ctx context.Context, config *schemas.WarpConfig, limits topicLimits, start, end time.Time, take func(topicVector) error) error {
	namespace := config.EffectiveLogVectorStoreNamespace()
	// Declared before reading. Indexing does this as it writes, but a process
	// that has indexed nothing since it started has not, and stores that keep
	// a namespace's dimension in memory cannot page it until it is.
	if _, err := ensureWarpNamespace(ctx, s.vectorStore, namespace, config.EmbeddingDimension); err != nil {
		return fmt.Errorf("prepare log namespace: %w", err)
	}
	reader := &topicWindowReader{
		store: s.vectorStore, namespace: namespace, limits: limits, take: take,
		// Inclusive of the window's last second.
		from: start.Unix(), to: end.Unix() + 1,
		seen: map[uint64]struct{}{},
		ctx:  vectorstore.WithIncludeVectors(vectorstore.WithDisableScanFallback(ctx)),
	}
	var err error
	if vectorstore.FiltersVectorReadsOnServer(s.vectorStore) {
		err = reader.readSlices()
	} else {
		_, err = reader.readRange(0, 0, false)
	}
	if errors.Is(err, errTopicReadLimit) {
		return err
	}
	if err != nil {
		return err
	}
	if reader.taken == 0 && reader.withoutVector > 0 {
		// Not an empty window: the requests are there, and saying otherwise
		// sends an operator looking for data that is not missing.
		return fmt.Errorf("%w: the vector store found %d request(s) in this window but did not return their vectors, which topic clustering needs", ErrUnavailable, reader.withoutVector)
	}
	return nil
}

type topicWindowReader struct {
	ctx       context.Context
	store     vectorstore.VectorStore
	namespace string
	limits    topicLimits
	from, to  int64
	take      func(topicVector) error
	// seen is the requests already taken, by a hash of their id. A slice that
	// turns out too large is read again as two, and a store that walks its
	// keys can hand the same entry back twice; a request clustered twice
	// counts twice.
	seen          map[uint64]struct{}
	taken         int
	withoutVector int
}

// readSlices reads the window a slice at a time.
func (r *topicWindowReader) readSlices() error {
	span := r.to - r.from
	if span <= 0 {
		return nil
	}
	// Find out how dense the window is from a part of it, widening until the
	// part holds something or is the whole window.
	width := max(span/256, 1)
	probeFrom := r.from + (span-width)/2
	found := 0
	for {
		count, err := r.readRange(probeFrom, probeFrom+width, true)
		if err != nil {
			return err
		}
		found = count
		if found > 0 || width >= span {
			break
		}
		width = min(width*4, span)
		probeFrom = max(r.from, min(probeFrom-width/2, r.to-width))
	}
	if found == 0 {
		return nil
	}
	if found < r.limits.slice {
		width = min(width*int64(r.limits.slice)/int64(found), span)
	}
	slices := int((span + width - 1) / width)
	for _, index := range spreadOrder(slices) {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		from := r.from + int64(index)*width
		if err := r.readSplitting(from, min(from+width, r.to)); err != nil {
			return err
		}
	}
	return nil
}

// readSplitting reads one slice, as two if it holds more than a slice may.
func (r *topicWindowReader) readSplitting(from, to int64) error {
	count, err := r.readRange(from, to, true)
	if err != nil {
		return err
	}
	if count < r.limits.sliceCeiling || to-from <= 1 {
		return nil
	}
	middle := from + (to-from)/2
	if err := r.readSplitting(from, middle); err != nil {
		return err
	}
	return r.readSplitting(middle, to)
}

// readRange pages [from, to) of the window, or the whole namespace when it is
// not filtered, and reports how many entries the store returned. A filtered
// read stops at the slice ceiling: what is left of the slice is read again as
// part of a smaller one.
func (r *topicWindowReader) readRange(from, to int64, filtered bool) (int, error) {
	var queries []vectorstore.Query
	if filtered {
		queries = []vectorstore.Query{
			{Field: "timestamp", Operator: vectorstore.QueryOperatorGreaterThanOrEqual, Value: from},
			{Field: "timestamp", Operator: vectorstore.QueryOperatorLessThan, Value: to},
			{Field: "warp_log", Operator: vectorstore.QueryOperatorEqual, Value: true},
		}
	}
	var cursor *string
	returned := 0
	for {
		if err := r.ctx.Err(); err != nil {
			return returned, err
		}
		results, next, err := r.store.GetAll(r.ctx, r.namespace, queries, []string{"log_id", "timestamp", "warp_log"}, cursor, r.limits.page)
		if err != nil {
			return returned, fmt.Errorf("read log vectors: %w", err)
		}
		returned += len(results)
		for _, result := range results {
			logID, _ := result.Properties["log_id"].(string)
			at := int64(propertyInt(result.Properties["timestamp"]))
			if logID == "" || !propertyBool(result.Properties["warp_log"]) || at < r.from || at >= r.to {
				continue
			}
			key := hashTopicID(logID)
			if _, repeat := r.seen[key]; repeat {
				continue
			}
			r.seen[key] = struct{}{}
			if len(result.Vector) == 0 {
				r.withoutVector++
				continue
			}
			if r.limits.read > 0 && r.taken >= r.limits.read {
				return returned, errTopicReadLimit
			}
			r.taken++
			if err := r.take(topicVector{logID: logID, vector: unitVector(result.Vector)}); err != nil {
				return returned, err
			}
		}
		if next == nil || len(results) == 0 || (filtered && returned >= r.limits.sliceCeiling) {
			return returned, nil
		}
		cursor = next
	}
}

func hashTopicID(id string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(id))
	return hash.Sum64()
}

// spreadOrder is 0..n-1 in an order whose every prefix is spread across the
// range: the indexes with their bits reversed. Reading slices in it means a
// run that stops partway has covered the window evenly, at whatever point it
// stopped.
func spreadOrder(n int) []int {
	if n <= 0 {
		return nil
	}
	width := bits.Len(uint(n - 1))
	order := make([]int, 0, n)
	for index := 0; index < 1<<width; index++ {
		reversed := int(bits.Reverse(uint(index)) >> (bits.UintSize - width))
		if width == 0 {
			reversed = 0
		}
		if reversed < n {
			order = append(order, reversed)
		}
	}
	return order
}
