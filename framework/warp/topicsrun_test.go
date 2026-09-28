package warp

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// topicTestVectorsFor names each point after the subject it was drawn around,
// so a cluster can be read back as the subjects it holds.
func topicTestVectorsFor(points [][]float32, perSubject int) []topicVector {
	vectors := make([]topicVector, len(points))
	for index, point := range points {
		vectors[index] = topicVector{logID: fmt.Sprintf("s%d-%03d", index/perSubject, index%perSubject), vector: point}
	}
	return vectors
}

// topicTestClusters builds clusters from a fixed assignment.
func topicTestClusters(vectors []topicVector, assignments []int, count int) []topicCluster {
	return summarizeClusters(vectors, assignments, make([][]float32, count), nil)
}

// subjectsOf is the subjects a cluster's samples were drawn from.
func subjectsOf(cluster topicCluster) []string {
	subjects := map[string]struct{}{}
	for _, id := range cluster.sampleIDs {
		subjects[strings.SplitN(id, "-", 2)[0]] = struct{}{}
	}
	names := make([]string, 0, len(subjects))
	for subject := range subjects {
		names = append(names, subject)
	}
	sort.Strings(names)
	return names
}

// Asking k-means for more clusters than there are subjects splits a subject
// across several of them. The merge has to put those back together, and
// leave apart the subjects that really are different - with a threshold it
// worked out from the run, not one written for this embedding space.
func TestMergeTopicClustersRejoinsOverSplitTopics(t *testing.T) {
	const dimension, perSubject = 64, 40
	rng := rand.New(rand.NewSource(7))
	directions := [][]float32{
		topicTestDirection(dimension, 0, 0, 0),
		topicTestDirection(dimension, 1, 1, 0),
		topicTestDirection(dimension, 2, 2, 0),
	}
	points := topicTestPoints(rng, directions, perSubject, 0.5)
	assignments, centroids, err := kMeans(context.Background(), points, 9, topicsKMeansIterations, 3)
	require.NoError(t, err)
	clusters := summarizeClusters(topicTestVectorsFor(points, perSubject), assignments, centroids, nil)
	require.Len(t, clusters, 9, "the run starts over-clustered")

	threshold, ok := topicMergeThreshold(clusters)
	require.True(t, ok)
	require.Greater(t, threshold, 0.5)
	require.Less(t, threshold, 1.0)

	merged, folded := mergeTopicClusters(clusters, threshold)
	require.Len(t, merged, 3, "one cluster per subject")
	require.Equal(t, 6, folded)
	seen := map[string]bool{}
	for _, cluster := range merged {
		require.Equal(t, perSubject, cluster.size)
		subjects := subjectsOf(cluster)
		require.Len(t, subjects, 1, "a cluster holds one subject")
		seen[subjects[0]] = true
	}
	require.Len(t, seen, 3)
}

// Related is not the same. Two subjects whose centres are clearly apart must
// survive the merge even though they are far more alike than unrelated ones.
func TestMergeTopicClustersKeepsRelatedTopicsApart(t *testing.T) {
	const dimension, perSubject = 64, 40
	rng := rand.New(rand.NewSource(11))
	directions := [][]float32{
		topicTestDirection(dimension, 0, 0, 0),
		topicTestDirection(dimension, 1, 0, 0.6),
	}
	points := topicTestPoints(rng, directions, perSubject, 0.4)
	assignments := make([]int, len(points))
	for index := range points {
		assignments[index] = index / perSubject
	}
	clusters := topicTestClusters(topicTestVectorsFor(points, perSubject), assignments, 2)

	threshold, ok := topicMergeThreshold(clusters)
	require.True(t, ok)
	merged, folded := mergeTopicClusters(clusters, threshold)
	require.Len(t, merged, 2)
	require.Zero(t, folded)
}

// A is close to B and B is close to C, but A and C are not alike. Joining
// everything that is linked would put all three in one topic; merging the
// closest pair and measuring again from the merged centre does not.
func TestMergeTopicClustersDoesNotChain(t *testing.T) {
	at := func(degrees float64) []float32 {
		radians := degrees * math.Pi / 180
		return []float32{float32(math.Cos(radians)), float32(math.Sin(radians))}
	}
	vectors := topicTestVectorsFor([][]float32{at(0), at(0), at(20), at(20), at(42), at(42)}, 2)
	clusters := topicTestClusters(vectors, []int{0, 0, 1, 1, 2, 2}, 3)

	merged, folded := mergeTopicClusters(clusters, math.Cos(25*math.Pi/180))
	require.Len(t, merged, 2)
	require.Equal(t, 1, folded)
	require.Equal(t, []string{"s0", "s1"}, subjectsOf(merged[0]), "the closest pair is joined")
	require.Equal(t, []string{"s2"}, subjectsOf(merged[1]), "the third is 32 degrees from the merged centre, outside the threshold")
}

// A cluster of one is perfectly similar to itself, which says nothing about
// how tight a topic is. Counting it would push the threshold toward 1 and
// switch the merge off on exactly the runs that are most over-split.
func TestTopicMergeThresholdIgnoresSingletons(t *testing.T) {
	points := [][]float32{{1, 0}, unitVector([]float32{0.9, 0.1}), unitVector([]float32{0.9, -0.1}), {0, 1}}
	clusters := topicTestClusters(topicTestVectorsFor(points, 3), []int{0, 0, 0, 1}, 2)
	threshold, ok := topicMergeThreshold(clusters)
	require.True(t, ok)
	require.InDelta(t, clusters[0].cohesion(), threshold, 1e-9, "only the cluster with members to compare sets the threshold")

	singletons := topicTestClusters(topicTestVectorsFor([][]float32{{1, 0}, {0, 1}}, 1), []int{0, 1}, 2)
	_, ok = topicMergeThreshold(singletons)
	require.False(t, ok, "nothing but singletons leaves no threshold to derive")
}

// A run keeps a cluster's sum and count and lets its members go. Everything
// reported about the cluster afterwards comes from those two, so they have to
// say exactly what the members would have: the centre is the direction of the
// sum, and the cohesion its length over the count.
func TestTopicClusterIsExactWithoutItsMembers(t *testing.T) {
	const dimension, perSubject = 32, 50
	rng := rand.New(rand.NewSource(3))
	points := topicTestPoints(rng, [][]float32{topicTestDirection(dimension, 0, 0, 0), topicTestDirection(dimension, 1, 1, 0)}, perSubject, 0.6)
	assignments := make([]int, len(points))
	for index := range points {
		assignments[index] = index / perSubject
	}
	clusters := topicTestClusters(topicTestVectorsFor(points, perSubject), assignments, 2)

	measure := func(members [][]float32) (centre []float32, cohesion float64) {
		sum := make([]float32, dimension)
		for _, member := range members {
			for d, value := range member {
				sum[d] += value
			}
		}
		centre = unitVector(sum)
		for _, member := range members {
			cohesion += float64(dot(member, centre)) / float64(len(members))
		}
		return centre, cohesion
	}
	for index, members := range [][][]float32{points[:perSubject], points[perSubject:]} {
		centre, cohesion := measure(members)
		require.InDelta(t, 1, float64(dot(centre, clusters[index].centroid())), 1e-5)
		require.InDelta(t, cohesion, clusters[index].cohesion(), 1e-4)
		require.Len(t, clusters[index].samples, topicsLabelSamples)
		for position := 1; position < len(clusters[index].samples); position++ {
			require.GreaterOrEqual(t, dot(clusters[index].samples[position-1].vector, centre), dot(clusters[index].samples[position].vector, centre), "samples are nearest the centre first")
		}
	}

	// Merged, it is the cluster all the members together would have made.
	centre, cohesion := measure(points)
	clusters[0].absorb(clusters[1])
	require.Equal(t, 2*perSubject, clusters[0].size)
	require.InDelta(t, 1, float64(dot(centre, clusters[0].centroid())), 1e-5)
	require.InDelta(t, cohesion, clusters[0].cohesion(), 1e-4)
}

// Slices are read in an order whose every prefix covers the range evenly, so
// a run that stops clustering partway has sampled the whole window.
func TestSpreadOrderCoversTheRangeAtEveryPrefix(t *testing.T) {
	for _, n := range []int{1, 2, 3, 7, 16, 100} {
		order := spreadOrder(n)
		require.Len(t, order, n)
		sorted := append([]int(nil), order...)
		sort.Ints(sorted)
		for index, value := range sorted {
			require.Equal(t, index, value, "every index of %d appears once", n)
		}
	}
	require.Empty(t, spreadOrder(0))

	order := spreadOrder(64)
	for _, prefix := range []int{4, 8, 16} {
		quarters := map[int]int{}
		for _, index := range order[:prefix] {
			quarters[index/16]++
		}
		require.Len(t, quarters, 4)
		for _, count := range quarters {
			require.Equal(t, prefix/4, count, "the first %d slices fall evenly across the window", prefix)
		}
	}
}

// Clustering in batches has to find what one pass would have. Three subjects
// arrive mixed together and are clustered sixty at a time; each batch finds
// the three again, and the merge has to see that they are the same three.
func TestTopicRunMergesAcrossBatches(t *testing.T) {
	const dimension, perSubject = 64, 100
	rng := rand.New(rand.NewSource(21))
	directions := [][]float32{topicTestDirection(dimension, 0, 0, 0), topicTestDirection(dimension, 1, 1, 0), topicTestDirection(dimension, 2, 2, 0)}
	vectors := topicTestVectorsFor(topicTestPoints(rng, directions, perSubject, 0.5), perSubject)
	rng.Shuffle(len(vectors), func(i, j int) { vectors[i], vectors[j] = vectors[j], vectors[i] })

	run := newTopicRun(topicLimits{batch: 60, clustered: 1000}, 5)
	for _, entry := range vectors {
		require.NoError(t, run.add(context.Background(), entry))
	}
	require.False(t, run.merged, "nothing is merged until the run ends or stops clustering")
	require.Less(t, len(run.batch), 60, "a full batch is clustered and let go")
	require.NoError(t, run.finish(context.Background()))

	require.Equal(t, 5, run.batches)
	require.Equal(t, 300, run.clustered)
	require.Zero(t, run.assigned)
	require.Len(t, run.clusters, 3)
	total := run.dropped
	for _, cluster := range run.clusters {
		require.Len(t, subjectsOf(cluster), 1, "a topic holds one subject")
		require.InDelta(t, perSubject, cluster.size, 3)
		total += cluster.size
	}
	require.Equal(t, 300, total, "every request is in a topic or counted as dropped")
}

// Past its limit a run stops clustering: what it has found are the topics,
// and every request after that is given to the nearest of them. One that is
// like none of them is counted as matching no topic rather than forced into
// the least unlike.
func TestTopicRunAssignsWhatItDoesNotCluster(t *testing.T) {
	const dimension, perSubject = 64, 100
	rng := rand.New(rand.NewSource(33))
	directions := [][]float32{topicTestDirection(dimension, 0, 0, 0), topicTestDirection(dimension, 1, 1, 0)}
	vectors := topicTestVectorsFor(topicTestPoints(rng, directions, perSubject, 0.5), perSubject)
	rng.Shuffle(len(vectors), func(i, j int) { vectors[i], vectors[j] = vectors[j], vectors[i] })
	strangers := topicTestPoints(rng, [][]float32{topicTestDirection(dimension, 40, 40, 0)}, 7, 0.5)

	run := newTopicRun(topicLimits{batch: 40, clustered: 80}, 5)
	for _, entry := range vectors {
		require.NoError(t, run.add(context.Background(), entry))
	}
	for index, stranger := range strangers {
		require.NoError(t, run.add(context.Background(), topicVector{logID: fmt.Sprintf("stranger-%d", index), vector: stranger}))
	}
	require.NoError(t, run.finish(context.Background()))

	require.True(t, run.merged)
	require.Equal(t, 2, run.batches)
	require.Equal(t, 80, run.clustered)
	require.Equal(t, 207, run.read())
	require.Equal(t, 127, run.assigned+run.unmatched, "what was not clustered was assigned or matched nothing")
	require.GreaterOrEqual(t, run.unmatched, 7, "the strangers match no topic")
	// The floor lets through all but the least alike few of what it was
	// measured on, so a few of each subject's own fall under it too.
	require.LessOrEqual(t, run.unmatched, 7+12, "and nearly everything else matches one")

	require.GreaterOrEqual(t, len(run.clusters), 2)
	inTopics := 0
	for index, cluster := range run.clusters {
		for _, id := range cluster.sampleIDs {
			require.NotContains(t, id, "stranger")
		}
		require.Len(t, subjectsOf(cluster), 1)
		inTopics += cluster.size
		if index < 2 {
			require.InDelta(t, perSubject, cluster.size, 12, "a topic counts what was assigned to it with what was clustered")
			require.Greater(t, cluster.cohesion(), 0.8, "and its cohesion is measured over both")
		}
	}
	require.NotEqual(t, subjectsOf(run.clusters[0]), subjectsOf(run.clusters[1]))
	require.Equal(t, run.read(), inTopics+run.dropped+run.unmatched, "every request is accounted for")
}

func topicWindowTestStore(t *testing.T, start time.Time, requests int) (*fakeWarpVectorStore, map[string]logstore.Log) {
	t.Helper()
	vectors := newFakeWarpVectorStore()
	logs := map[string]logstore.Log{}
	for index := range requests {
		id := fmt.Sprintf("log-%04d", index)
		at := start.Add(time.Duration(index) * time.Minute)
		subject, question := 0, "how do I get a refund"
		if index%2 == 1 {
			subject, question = 1, "reset my password please"
		}
		vector := []float32{0.02 * float32(index%5), 0, 0.01 * float32(index%3)}
		vector[subject] = 1
		logs[id] = logstore.Log{ID: id, Timestamp: at, Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: question}
		require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, id, unitVector(vector), map[string]interface{}{"log_id": id, "timestamp": at.Unix(), "warp_log": true}))
	}
	return vectors, logs
}

// A store that filters on the server is read a slice of the window at a time,
// so a read costs what the window holds and not what the namespace does. A
// slice that turns out to hold more than one read may is read again as two,
// which hands some requests over twice: each still counts once. What falls
// outside the window is never asked for.
func TestTopicWindowIsReadInSlices(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	vectors, _ := topicWindowTestStore(t, start, 600)
	vectors.filtersOnServer = true
	service := NewService(nil, WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithVectorStore(vectors))
	defer service.Shutdown()

	// Minutes 100 to 399 of the 600, read with slices far smaller than it.
	from, to := start.Add(100*time.Minute), start.Add(399*time.Minute)
	limits := topicLimits{batch: 1000, clustered: 1000, slice: 20, sliceCeiling: 30, page: 10}
	taken := map[string]int{}
	require.NoError(t, service.readTopicWindow(context.Background(), configFromRow(validWarpConfigRow()), limits, from, to, func(entry topicVector) error {
		taken[entry.logID]++
		require.InDelta(t, 1, float64(dot(entry.vector, entry.vector)), 1e-5, "vectors arrive at unit length")
		return nil
	}))
	require.Len(t, taken, 300)
	for index := 100; index <= 399; index++ {
		require.Equal(t, 1, taken[fmt.Sprintf("log-%04d", index)], "request %d is taken once", index)
	}

	require.Greater(t, len(vectors.reads), 10, "the window is read in many slices")
	for _, queries := range vectors.reads {
		require.Len(t, queries, 3, "every read is filtered")
		low, high := int64(propertyInt(queries[0].Value)), int64(propertyInt(queries[1].Value))
		require.Equal(t, "timestamp", queries[0].Field)
		require.GreaterOrEqual(t, low, from.Unix())
		require.LessOrEqual(t, high, to.Unix()+1)
		require.Less(t, low, high)
	}
}

// A store that does not filter on the server pays for the whole namespace on
// every filtered read, so slices would cost it the namespace each. It is read
// once, unfiltered, and the window applied to what comes back.
func TestTopicWindowIsReadOnceFromAStoreThatCannotFilter(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	vectors, _ := topicWindowTestStore(t, start, 600)
	service := NewService(nil, WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithVectorStore(vectors))
	defer service.Shutdown()

	limits := topicLimits{batch: 1000, clustered: 1000, slice: 20, sliceCeiling: 30, page: 50}
	taken := map[string]int{}
	require.NoError(t, service.readTopicWindow(context.Background(), configFromRow(validWarpConfigRow()), limits, start.Add(100*time.Minute), start.Add(399*time.Minute), func(entry topicVector) error {
		taken[entry.logID]++
		return nil
	}))
	require.Len(t, taken, 300)
	require.Len(t, vectors.reads, 1, "one pass over the namespace")
	require.Empty(t, vectors.reads[0], "with no filter")
}

// The job reports what it did with everything it read: how much it
// clustered, how much it gave to the nearest topic after that, and how much
// ended in no topic. Sizes and shares are of all of it.
func TestWarpTopicsJobReportsClusteredAndAssigned(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for name, filtersOnServer := range map[string]bool{"a store that filters": true, "a store that does not": false} {
		t.Run(name, func(t *testing.T) {
			vectors, logs := topicWindowTestStore(t, start, 400)
			vectors.filtersOnServer = filtersOnServer
			service := NewService(nil,
				WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(&semanticLogReader{logs: logs}),
				WithVectorStore(vectors), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithChatFunc(topicTestNaming),
			)
			defer service.Shutdown()
			service.topicLimits = topicLimits{batch: 50, clustered: 100, slice: 40, sliceCeiling: 60, page: 20}

			metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
			require.NoError(t, err)
			var messages []string
			finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(metadata string) error {
				var meta BackfillJobMeta
				require.NoError(t, sonic.Unmarshal([]byte(metadata), &meta))
				messages = append(messages, meta.Message)
				return nil
			})
			require.NoError(t, err)

			var final BackfillJobMeta
			require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
			require.Equal(t, 400, final.Scanned)
			require.Equal(t, 100, final.Clustered)
			require.InDelta(t, 300, final.Assigned, 20, "what was not clustered was assigned")
			require.Less(t, final.Unassigned, 20)
			require.Equal(t, 2, final.Indexed)
			require.Contains(t, final.Message, "Clustered 100 request(s) into 2 topic(s).")
			require.Contains(t, final.Message, "were given to the nearest topic")
			require.Contains(t, strings.Join(messages, "\n"), "assigning the rest")

			inTopics := 0
			for _, topic := range final.Topics {
				require.InDelta(t, 200, topic.Size, 20)
				inTopics += topic.Size
			}
			require.Equal(t, 400, inTopics+final.Unassigned)

			topics, total, err := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, vectors).ListTopics(context.Background(), 15)
			require.NoError(t, err)
			require.Equal(t, 400, total, "shares are of everything the run read")
			require.Len(t, topics, 2)
			require.InDelta(t, 0.5, topics[0].Share, 0.05)
		})
	}
}

// One run takes in a bounded number of requests. A window that holds more is
// not read to the end: the run stops at the bound, says that it did, and what
// it clustered is drawn from across the window rather than from its start.
func TestWarpTopicsJobStopsAtItsReadLimit(t *testing.T) {
	require.Equal(t, 20000, defaultTopicLimits().read, "the bound a deployment runs with")
	require.Equal(t, defaultTopicLimits().read, defaultTopicLimits().clustered, "within it, everything read is clustered")

	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for name, filtersOnServer := range map[string]bool{"a store that filters": true, "a store that does not": false} {
		t.Run(name, func(t *testing.T) {
			vectors, logs := topicWindowTestStore(t, start, 400)
			vectors.filtersOnServer = filtersOnServer
			service := NewService(nil,
				WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(&semanticLogReader{logs: logs}),
				WithVectorStore(vectors), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithChatFunc(topicTestNaming),
			)
			defer service.Shutdown()
			service.topicLimits = topicLimits{batch: 150, clustered: 150, read: 150, slice: 40, sliceCeiling: 60, page: 20}

			metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
			require.NoError(t, err)
			finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
			require.NoError(t, err)
			var final BackfillJobMeta
			require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
			require.Equal(t, 150, final.Scanned)
			require.Equal(t, 150, final.Clustered)
			require.Zero(t, final.Assigned)
			require.True(t, final.Capped)
			require.Contains(t, final.Message, "Stopped at the limit of 150 request(s) for one run")
			require.Equal(t, 2, final.Indexed)
		})
	}

	t.Run("a window within the bound is read whole and says nothing of it", func(t *testing.T) {
		vectors, logs := topicWindowTestStore(t, start, 100)
		service := NewService(nil,
			WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(&semanticLogReader{logs: logs}),
			WithVectorStore(vectors), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithChatFunc(topicTestNaming),
		)
		defer service.Shutdown()
		service.topicLimits = topicLimits{batch: 150, clustered: 150, read: 150, slice: 40, sliceCeiling: 60, page: 20}
		metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
		require.NoError(t, err)
		finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
		require.NoError(t, err)
		var final BackfillJobMeta
		require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
		require.Equal(t, 100, final.Scanned)
		require.False(t, final.Capped)
		require.NotContains(t, final.Message, "Stopped at the limit")
	})
}

// Most real topics are small beside the largest. A run that folded every
// cluster under a share of its requests into the nearest topic took a
// deployment's ninety topics down to nine, each a mix of subjects. In one
// batch a cluster stands or falls by the merge alone, however small it is
// beside the rest.
func TestTopicRunKeepsSmallTopicsBesideLargeOnes(t *testing.T) {
	const dimension, subjects, perSubject = 64, 12, 15
	rng := rand.New(rand.NewSource(17))
	large := topicTestPoints(rng, [][]float32{topicTestDirection(dimension, 0, 0, 0)}, 2000, 0.5)
	// A handful of requests each - well under a hundredth of the run - on
	// subjects of their own, in the same neighbourhood as the large one.
	var small [][]float32
	for subject := 1; subject <= subjects; subject++ {
		small = append(small, topicTestPoints(rng, [][]float32{topicTestDirection(dimension, subject, 0, 0.55)}, perSubject, 0.4)...)
	}
	vectors := topicTestVectorsFor(large, 2000)
	for index, point := range small {
		vectors = append(vectors, topicVector{logID: fmt.Sprintf("small%02d-%03d", index/perSubject, index%perSubject), vector: point})
	}
	rng.Shuffle(len(vectors), func(i, j int) { vectors[i], vectors[j] = vectors[j], vectors[i] })

	run := newTopicRun(topicLimits{batch: 5000, clustered: 5000}, 9)
	for _, entry := range vectors {
		require.NoError(t, run.add(context.Background(), entry))
	}
	require.NoError(t, run.finish(context.Background()))
	require.Equal(t, 1, run.batches)

	kept := map[string]int{}
	for _, cluster := range run.clusters {
		subjectsHeld := subjectsOf(cluster)
		if len(subjectsHeld) == 1 && strings.HasPrefix(subjectsHeld[0], "small") {
			kept[subjectsHeld[0]] += cluster.size
		}
	}
	require.Len(t, kept, subjects, "every small subject is still a topic of its own")
	for subject, size := range kept {
		require.InDelta(t, perSubject, size, 4, subject)
	}
}
