/*
Copyright 2020 The TestGrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package summarizer

import (
	"context"
	"regexp"

	"github.com/GoogleCloudPlatform/testgrid/internal/result"
	"github.com/GoogleCloudPlatform/testgrid/pb/state"
	summarypb "github.com/GoogleCloudPlatform/testgrid/pb/summary"
	"github.com/GoogleCloudPlatform/testgrid/pkg/summarizer/common"
)

const (
	minRuns = 0
)

var (
	infraRegex = regexp.MustCompile(`^\w+$`)
)

type flakinessAnalyzer interface {
	GetFlakiness(gridMetrics []*common.GridMetrics, minRuns int, startDate int, endDate int, tab string) *summarypb.HealthinessInfo
}

// CalculateHealthiness extracts the test run data from each row (which represents a test)
// of the Grid and then analyzes it with an implementation of flakinessAnalyzer, which has
// implementations in the subdir naive and can be injected as needed.
func CalculateHealthiness(grid *state.Grid, analyzer flakinessAnalyzer, startTime int, endTime int, tab string) *summarypb.HealthinessInfo {
	gridMetrics := parseGrid(grid, startTime, endTime)

	return analyzer.GetFlakiness(gridMetrics, minRuns, startTime, endTime, tab)
}

func parseGrid(grid *state.Grid, startTime int, endTime int) []*common.GridMetrics {
	// Get the relevant data for flakiness from each Grid (which represents
	// a dashboard tab) as a list of GridMetrics structs

	// TODO (itsazhuhere@): consider refactoring/using summary.go's gridMetrics function
	// as it does very similar data collection.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// We create maps because result.Map returns a map where we can access each result
	// through the test name, and at each instance we can increment our types.Result
	// using the same key. At the end we can filter out those types.Result that had
	// 0 of all counts.
	gridMetricsMap := make(map[string]*common.GridMetrics, 0)
	gridRows := make(map[string]*state.Row)
	for i, row := range grid.Rows {
		gridRows[row.Name] = grid.Rows[i]
	}
	// result.Map is written in a way that assumes each test/row name is unique
	rowResults := result.Map(ctx, grid.Rows)

	for i := 0; i < len(grid.Columns); i++ {
		if !isWithinTimeFrame(grid.Columns[i], startTime, endTime) {
			continue
		}

		for key, ch := range rowResults {
			if _, ok := gridMetricsMap[key]; !ok {
				gridMetricsMap[key] = common.NewGridMetrics(key)
			}

			switch result.Coalesce(<-ch, result.IgnoreRunning) {
			case state.Row_NO_RESULT:
				continue
			case state.Row_FAIL:
				categorizeFailure(gridMetricsMap[key], gridRows[key].Messages[i])
			case state.Row_PASS:
				gridMetricsMap[key].Passed++
			case state.Row_FLAKY:
				getValueOfFlakyMetric(gridMetricsMap[key])
			}
		}
	}
	gridMetrics := make([]*common.GridMetrics, 0)
	for _, metric := range gridMetricsMap {
		if metric.Failed > 0 || metric.Passed > 0 || metric.FlakyCount > 0 {
			gridMetrics = append(gridMetrics, metric)
		}
	}
	return gridMetrics
}

func categorizeFailure(resultCounts *common.GridMetrics, message string) {
	if message == "" || !infraRegex.MatchString(message) {
		resultCounts.Failed++
		return
	}
	resultCounts.FailedInfraCount++
	resultCounts.InfraFailures[message] = resultCounts.InfraFailures[message] + 1
}

func getValueOfFlakyMetric(gridMetrics *common.GridMetrics) {
	// TODO (itszhuhere@): add a way to get exact flakiness from a Row_FLAKY cell
	// For now we will leave it as 50%, because:
	// a) gridMetrics.flakiness and .flakyCount are currently not used by anything
	// and
	// b) there's no easy way to get the exact flakiness measurement from prow or whatever else
	// and potentially
	// c) GKE does not currently enable retry on flakes so it isn't as important right now
	// Keep in mind that flakiness is measured as out of 100, i.e. 23 not .23
	flakiness := 50.0
	gridMetrics.FlakyCount++
	// Formula for adding one new value to mean is mean + (newValue - mean) / newCount
	gridMetrics.AverageFlakiness += (flakiness - gridMetrics.AverageFlakiness) / float64(gridMetrics.FlakyCount)
}

func isWithinTimeFrame(column *state.Column, startTime, endTime int) bool {
	return column.Started >= float64(startTime) && column.Started <= float64(endTime)
}
