package traceqlmetrics

import (
	"context"
	"testing"

	"github.com/grafana/tempo/pkg/traceql"
	"github.com/stretchr/testify/require"
)

func TestPercentile(t *testing.T) {

	testCases := []struct {
		name      string
		durations []uint64
		p         float64
		value     uint64
	}{
		{
			name:      "easy mode",
			durations: []uint64{2, 4, 6, 8},
			p:         0.5,
			value:     uint64(4),
		},
		{
			// 10 samples
			// p75 rounds means 7.5 samples, rounds up to 8
			// 5 samples from the 2048 bucket
			// 3 samples from the 4096 bucket
			// interpolation: 3/5ths from 2048 to 4096 exponentially
			// = 2048 * 2^0.6 = 3104.1875...
			name:      "interpolate between buckets",
			durations: []uint64{2000, 2000, 2000, 2000, 2000, 4000, 4000, 4000, 4000, 4000},
			p:         0.75,
			value:     uint64(3104),
		},
		{
			name:      "edge case bucket 0",
			durations: []uint64{1},
			p:         1.0,
			value:     uint64(1),
		},
		{
			name:      "edge case empty",
			durations: []uint64{},
			p:         1.0,
			value:     uint64(0),
		},
	}

	for _, tc := range testCases {
		m := &LatencyHistogram{}
		for _, d := range tc.durations {
			m.Record(d)
		}
		got := m.Percentile(tc.p)
		require.Equal(t, tc.value, got, tc.name)
	}
}

func TestMetricsResultsCombine(t *testing.T) {
	a := traceql.NewStaticString("1")
	b := traceql.NewStaticString("2")
	c := traceql.NewStaticString("3")

	m := NewMetricsResults()
	m.Record(a, 1, true)
	m.Record(b, 1, false)
	m.Record(b, 1, false)
	m.Record(b, 1, true)

	m2 := NewMetricsResults()
	m2.Record(b, 1, true)
	m2.Record(c, 1, false)
	m2.Record(c, 1, false)
	m2.Record(c, 1, true)

	m.Combine(m2)

	require.Equal(t, 3, len(m.Series))
	require.Equal(t, 3, len(m.Errors))

	require.Equal(t, 1, m.Series[a].Count())
	require.Equal(t, 4, m.Series[b].Count())
	require.Equal(t, 3, m.Series[c].Count())

	require.Equal(t, 1, m.Errors[a])
	require.Equal(t, 2, m.Errors[b])
	require.Equal(t, 1, m.Errors[c])
}

func TestGetMetrics(t *testing.T) {

	var (
		ctx     = context.TODO()
		query   = "{}"
		groupBy = "span.foo"
		start   = uint64(0)
		end     = uint64(0)
	)

	m := &mockFetcher{
		Spansets: []*traceql.Spanset{
			{
				Spans: []traceql.Span{
					newMockSpan().WithDuration(128).WithAttributes("span.foo", "1"),
					newMockSpan().WithDuration(128).WithAttributes("span.foo", "1"), // p50 for foo=1
					newMockSpan().WithDuration(256).WithAttributes("span.foo", "1"),
					newMockSpan().WithDuration(256).WithAttributes("span.foo", "1"),
					newMockSpan().WithDuration(256).WithAttributes("span.foo", "2"),
					newMockSpan().WithDuration(256).WithAttributes("span.foo", "2"), // p50 for foo=2
					newMockSpan().WithDuration(512).WithAttributes("span.foo", "2"),
					newMockSpan().WithDuration(512).WithAttributes("span.foo", "2").WithErr(),
				},
			},
		},
	}

	res, err := GetMetrics(ctx, query, groupBy, 1000, start, end, m)
	require.NoError(t, err)
	require.NotNil(t, res)

	one := traceql.NewStaticString("1")
	two := traceql.NewStaticString("2")

	require.Equal(t, 0, res.Errors[one])
	require.Equal(t, 1, res.Errors[two])

	require.NotNil(t, res.Series[one])
	require.NotNil(t, res.Series[two])

	require.Equal(t, uint64(128), res.Series[one].Percentile(0.5))  // p50
	require.Equal(t, uint64(181), res.Series[one].Percentile(0.75)) // p75, 128 * 2^0.5 = 181
	require.Equal(t, uint64(256), res.Series[one].Percentile(1.0))  // p100

	require.Equal(t, uint64(256), res.Series[two].Percentile(0.5))  // p50
	require.Equal(t, uint64(362), res.Series[two].Percentile(0.75)) // p75, 256 * 2^0.5 = 362
	require.Equal(t, uint64(512), res.Series[two].Percentile(1.0))  // p100
}

func TestGetMetricsTimeRange(t *testing.T) {

	var (
		ctx     = context.TODO()
		query   = "{}"
		groupBy = "span.foo"
		start   = uint64(100)
		end     = uint64(200)
	)

	m := &mockFetcher{
		Spansets: []*traceql.Spanset{
			{
				Spans: []traceql.Span{
					newMockSpan().WithStart(100).WithDuration(128).WithAttributes("span.foo", "1"),  // Included
					newMockSpan().WithStart(200).WithDuration(256).WithAttributes("span.foo", "1"),  // not included
					newMockSpan().WithStart(100).WithDuration(512).WithAttributes("span.foo", "2"),  // Included
					newMockSpan().WithStart(200).WithDuration(1024).WithAttributes("span.foo", "2"), // not included
				},
			},
		},
	}

	res, err := GetMetrics(ctx, query, groupBy, 1000, start, end, m)
	require.NoError(t, err)
	require.NotNil(t, res)

	one := traceql.NewStaticString("1")
	two := traceql.NewStaticString("2")

	require.Equal(t, uint64(128), res.Series[one].Percentile(1.0)) // Highest span
	require.Equal(t, uint64(512), res.Series[two].Percentile(1.0)) // Highest span
}
