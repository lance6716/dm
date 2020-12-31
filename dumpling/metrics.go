// Copyright 2020 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package dumpling

import (
	"github.com/pingcap/dumpling/v4/export"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/pingcap/dm/pkg/metricsproxy"
)

var (
	// should alert
	dumplingExitWithErrorCounter = metricsproxy.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "dm",
			Subsystem: "dumpling",
			Name:      "exit_with_error_count",
			Help:      "counter for dumpling exit with error",
		}, []string{"task", "source_id"})
)

// RegisterMetrics registers metrics.
func RegisterMetrics(registry *prometheus.Registry) {
	registry.MustRegister(dumplingExitWithErrorCounter)
	export.RegisterMetrics(registry)
}

func (m *Dumpling) removeLabelValuesWithTaskInMetrics(task string) {
	dumplingExitWithErrorCounter.DeleteAllAboutLabels(prometheus.Labels{"task": task})
	export.RemoveLabelValuesWithTaskInMetrics(prometheus.Labels{"task": task})
}
