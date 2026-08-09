package prom

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// legendToken matches a single `{{ label }}` reference in a legend template,
// tolerating whitespace around the label name.
var legendToken = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// FormatName renders a series name from a legend template against the metric
// labels (report §9.3). An empty template means the metric's `__name__`, or
// "series" when it has none. Unknown `{{ label }}` references expand to "".
func FormatName(tmpl string, labels map[string]string) string {
	if tmpl == "" {
		if name, ok := labels["__name__"]; ok && name != "" {
			return name
		}
		return "series"
	}
	name := tmpl
	// Pre-validate {{ }} pairs so an unbalanced template falls back to the
	// default rather than emitting literal `{{` garbage.
	if strings.Count(name, "{{") != strings.Count(name, "}}") {
		return FormatName("", labels)
	}
	return legendToken.ReplaceAllStringFunc(name, func(m string) string {
		sub := legendToken.FindStringSubmatch(m)
		if len(sub) != 2 {
			return m
		}
		return labels[strings.TrimSpace(sub[1])]
	})
}

// matrixToFrame converts a matrix result into a WIDE time-series frame
// (report §9.3): one shared time.Time field plus one *float64 field per series
// with its labels. Timestamps are aligned in query_range responses, so a wide
// frame is exact. Zero series yield a frame with only the time field.
func matrixToFrame(d queryData, legend string) (*data.Frame, error) {
	series := make([]promSeries, 0, len(d.Result))
	for _, raw := range d.Result {
		var s promSeries
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("prometheus matrix result: %w", err)
		}
		series = append(series, s)
	}

	// Timestamps from the first non-empty series define the shared axis.
	var times []time.Time
	for _, s := range series {
		if len(s.Values) == 0 {
			continue
		}
		times = make([]time.Time, len(s.Values))
		for i, smp := range s.Values {
			times[i] = sampleTime(smp)
		}
		break
	}

	frame := data.NewFrame("prometheus")
	frame.Fields = append(frame.Fields, data.NewField("time", nil, times))

	for _, s := range series {
		values := make([]*float64, len(times))
		for j, smp := range s.Values {
			if j >= len(values) {
				break
			}
			f, ok := sampleValue(smp)
			if ok {
				values[j] = &f
			}
		}
		name := FormatName(legend, s.Metric)
		labels := data.Labels(metricLabels(s.Metric))
		frame.Fields = append(frame.Fields, data.NewField(name, labels, values))
	}

	frame.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesWide}
	return frame, nil
}

// vectorToFrame converts a vector result into a wide single-row frame
// (report §9.4) for stat/gauge/table: one time value plus one *float64 field
// per series. Instant queries evaluate at a single timestamp, so a wide frame
// is exact.
func vectorToFrame(d queryData, legend string) (*data.Frame, error) {
	var series []promSeries
	for _, raw := range d.Result {
		var s promSeries
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("prometheus vector result: %w", err)
		}
		series = append(series, s)
	}

	var at time.Time
	if len(series) > 0 && series[0].Value != nil {
		at = sampleTime(*series[0].Value)
	}

	frame := data.NewFrame("prometheus")
	frame.Fields = append(frame.Fields, data.NewField("time", nil, []*time.Time{&at}))

	for _, s := range series {
		var value *float64
		if s.Value != nil {
			if f, ok := sampleValue(*s.Value); ok {
				value = &f
			}
		}
		name := FormatName(legend, s.Metric)
		labels := data.Labels(metricLabels(s.Metric))
		frame.Fields = append(frame.Fields, data.NewField(name, labels, []*float64{value}))
	}
	return frame, nil
}

// metricLabels strips the internal __name__ label (it names the field) from the
// labels attached to the field (report §9.3).
func metricLabels(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k == "__name__" {
			continue
		}
		out[k] = v
	}
	return out
}

func sampleTime(s promSample) time.Time {
	if ts, ok := s[0].(float64); ok {
		return time.Unix(int64(ts), 0).UTC()
	}
	return time.Unix(0, 0).UTC()
}

func sampleValue(s promSample) (float64, bool) {
	str, ok := s[1].(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(str, 64)
	if err != nil || math.IsNaN(f) {
		// NaN samples are dropped (Prometheus stores them for staleness).
		return 0, false
	}
	return f, true
}
