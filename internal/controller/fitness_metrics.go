package controller

import (
	"github.com/prometheus/client_golang/prometheus"
)

const metricsNamespace = "sympozium"
const metricsSubsystem = "fitness"

// FitnessMetrics exposes Prometheus metrics derived from the FitnessCache.
// Implements prometheus.Collector so it can be registered with any registry.
// Metrics are computed on-the-fly from the cache at scrape time.
type FitnessMetrics struct {
	cache *FitnessCache

	nodeScore        *prometheus.Desc
	nodeStale        *prometheus.Desc
	nodeRAMTotal     *prometheus.Desc
	nodeRAMAvailable *prometheus.Desc
	nodeGPUVRAM      *prometheus.Desc
	nodeGPUCount     *prometheus.Desc
	nodeModelCount   *prometheus.Desc
	clusterNodes     *prometheus.Desc
	clusterStale     *prometheus.Desc
}

// NewFitnessMetrics creates a FitnessMetrics collector for the given cache.
func NewFitnessMetrics(cache *FitnessCache) *FitnessMetrics {
	return &FitnessMetrics{
		cache: cache,
		nodeScore: prometheus.NewDesc(
			prometheus.BuildFQName(metricsNamespace, metricsSubsystem, "node_score"),
			"Highest model fitness score for a node",
			[]string{"node"}, nil,
		),
		nodeStale: prometheus.NewDesc(
			prometheus.BuildFQName(metricsNamespace, metricsSubsystem, "node_stale"),
			"Whether a node's fitness data is stale (1=stale, 0=fresh)",
			[]string{"node"}, nil,
		),
		nodeRAMTotal: prometheus.NewDesc(
			prometheus.BuildFQName(metricsNamespace, metricsSubsystem, "node_ram_total_gb"),
			"Total RAM in GB for a node",
			[]string{"node"}, nil,
		),
		nodeRAMAvailable: prometheus.NewDesc(
			prometheus.BuildFQName(metricsNamespace, metricsSubsystem, "node_ram_available_gb"),
			"Available RAM in GB for a node",
			[]string{"node"}, nil,
		),
		nodeGPUVRAM: prometheus.NewDesc(
			prometheus.BuildFQName(metricsNamespace, metricsSubsystem, "node_gpu_vram_gb"),
			"GPU VRAM in GB for a node",
			[]string{"node"}, nil,
		),
		nodeGPUCount: prometheus.NewDesc(
			prometheus.BuildFQName(metricsNamespace, metricsSubsystem, "node_gpu_count"),
			"Number of GPUs on a node",
			[]string{"node"}, nil,
		),
		nodeModelCount: prometheus.NewDesc(
			prometheus.BuildFQName(metricsNamespace, metricsSubsystem, "node_model_count"),
			"Number of models that fit on a node",
			[]string{"node"}, nil,
		),
		clusterNodes: prometheus.NewDesc(
			prometheus.BuildFQName(metricsNamespace, metricsSubsystem, "cluster_nodes_total"),
			"Total number of nodes reporting fitness data",
			nil, nil,
		),
		clusterStale: prometheus.NewDesc(
			prometheus.BuildFQName(metricsNamespace, metricsSubsystem, "cluster_nodes_stale"),
			"Number of nodes with stale fitness data",
			nil, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (m *FitnessMetrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.nodeScore
	ch <- m.nodeStale
	ch <- m.nodeRAMTotal
	ch <- m.nodeRAMAvailable
	ch <- m.nodeGPUVRAM
	ch <- m.nodeGPUCount
	ch <- m.nodeModelCount
	ch <- m.clusterNodes
	ch <- m.clusterStale
}

// Collect implements prometheus.Collector. Reads from the cache at scrape time.
func (m *FitnessMetrics) Collect(ch chan<- prometheus.Metric) {
	m.cache.mu.RLock()
	defer m.cache.mu.RUnlock()

	var staleCount float64
	for name, nf := range m.cache.nodes {
		stale := m.cache.isStaleUnlocked(nf)

		// Best model score for this node.
		var bestScore float64
		for _, fit := range nf.ModelFits {
			if fit.Score > bestScore {
				bestScore = fit.Score
			}
		}

		ch <- prometheus.MustNewConstMetric(m.nodeScore, prometheus.GaugeValue, bestScore, name)

		staleVal := 0.0
		if stale {
			staleVal = 1.0
			staleCount++
		}
		ch <- prometheus.MustNewConstMetric(m.nodeStale, prometheus.GaugeValue, staleVal, name)
		ch <- prometheus.MustNewConstMetric(m.nodeRAMTotal, prometheus.GaugeValue, nf.System.TotalRAMGb, name)
		ch <- prometheus.MustNewConstMetric(m.nodeRAMAvailable, prometheus.GaugeValue, nf.System.AvailableRAMGb, name)

		gpuVRAM := 0.0
		if nf.System.GPUVRAMGb != nil {
			gpuVRAM = *nf.System.GPUVRAMGb
		}
		ch <- prometheus.MustNewConstMetric(m.nodeGPUVRAM, prometheus.GaugeValue, gpuVRAM, name)
		ch <- prometheus.MustNewConstMetric(m.nodeGPUCount, prometheus.GaugeValue, float64(nf.System.GPUCount), name)
		ch <- prometheus.MustNewConstMetric(m.nodeModelCount, prometheus.GaugeValue, float64(len(nf.ModelFits)), name)
	}

	ch <- prometheus.MustNewConstMetric(m.clusterNodes, prometheus.GaugeValue, float64(len(m.cache.nodes)))
	ch <- prometheus.MustNewConstMetric(m.clusterStale, prometheus.GaugeValue, staleCount)
}
