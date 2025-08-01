package queryrange

import (
	"context"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/grafana/dskit/user"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/loghttp"
	"github.com/grafana/loki/v3/pkg/logproto"
	logql_log "github.com/grafana/loki/v3/pkg/logql/log"
	"github.com/grafana/loki/v3/pkg/logql/syntax"
	"github.com/grafana/loki/v3/pkg/logqlmodel"
	base "github.com/grafana/loki/v3/pkg/querier/queryrange/queryrangebase"

	"github.com/grafana/loki/pkg/push"
)

func Test_parseDetectedFields(t *testing.T) {
	now := time.Now()

	t.Run("when no parsers are supplied", func(t *testing.T) {
		infoDetectdFiledMetadata := []push.LabelAdapter{
			{
				Name:  "detected_level",
				Value: "info",
			},
		}

		rulerLines := []push.Entry{
			{
				Timestamp:          now,
				Line:               "ts=2024-09-05T15:36:38.757788067Z caller=grpc_logging.go:66 tenant=2419 level=info method=/cortex.Ingester/Push duration=19.098s msg=gRPC",
				StructuredMetadata: infoDetectdFiledMetadata,
			},
			{
				Timestamp:          now,
				Line:               "ts=2024-09-05T15:36:38.698375619Z caller=grpc_logging.go:66 tenant=29 level=info method=/cortex.Ingester/Push duration=5.471s msg=gRPC",
				StructuredMetadata: infoDetectdFiledMetadata,
			},
			{
				Timestamp:          now,
				Line:               "ts=2024-09-05T15:36:38.629424175Z caller=grpc_logging.go:66 tenant=2919 level=info method=/cortex.Ingester/Push duration=29.234s msg=gRPC",
				StructuredMetadata: infoDetectdFiledMetadata,
			},
		}

		rulerLbls := `{cluster="us-east-1", namespace="mimir-dev", pod="mimir-ruler-nfb37", service_name="mimir-ruler"}`
		rulerMetric, err := parser.ParseMetric(rulerLbls)
		require.NoError(t, err)

		rulerStream := push.Stream{
			Labels:  rulerLbls,
			Entries: rulerLines,
			Hash:    labels.StableHash(rulerMetric),
		}

		debugDetectedFieldMetadata := []push.LabelAdapter{
			{
				Name:  "detected_level",
				Value: "debug",
			},
		}

		nginxJSONLines := []push.Entry{
			{
				Timestamp:          now,
				Line:               `{"host":"100.117.38.203", "user-identifier":"nader3722", "datetime":"05/Sep/2024:16:13:56 +0000", "method": "PATCH", "request": "/api/loki/v1/push", "protocol":"HTTP/2.0", "status":200, "bytes":9664, "referer": "https://www.seniorbleeding-edge.net/exploit/robust/whiteboard"}`,
				StructuredMetadata: debugDetectedFieldMetadata,
			},
			{
				Timestamp:          now,
				Line:               `{"host":"66.134.9.30", "user-identifier":"-", "datetime":"05/Sep/2024:16:13:55 +0000", "method": "DELETE", "request": "/api/mimir/v1/push", "protocol":"HTTP/1.1", "status":200, "bytes":18688, "referer": "https://www.districtiterate.biz/synergistic/next-generation/extend"}`,
				StructuredMetadata: debugDetectedFieldMetadata,
			},
			{
				Timestamp:          now,
				Line:               `{"host":"66.134.9.30", "user-identifier":"-", "datetime":"05/Sep/2024:16:13:55 +0000", "method": "GET", "request": "/api/loki/v1/label/names", "protocol":"HTTP/1.1", "status":200, "bytes":9314, "referer": "https://www.dynamicimplement.info/enterprise/distributed/incentivize/strategic"}`,
				StructuredMetadata: debugDetectedFieldMetadata,
			},
		}

		nginxLbls := `{ cluster="eu-west-1", level="debug", namespace="gateway", pod="nginx-json-oghco", service_name="nginx-json" }`
		nginxMetric, err := parser.ParseMetric(nginxLbls)
		require.NoError(t, err)

		nginxStream := push.Stream{
			Labels:  nginxLbls,
			Entries: nginxJSONLines,
			Hash:    labels.StableHash(nginxMetric),
		}

		t.Run("detects logfmt fields", func(t *testing.T) {
			df := parseDetectedFields(uint32(15), logqlmodel.Streams([]push.Stream{rulerStream}))
			for _, expected := range []string{"ts", "caller", "tenant", "level", "method", "duration", "msg"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1)
				require.Equal(t, "logfmt", parsers[0])
			}

			// no parsers for structed metadata
			for _, expected := range []string{"detected_level"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 0)
			}
		})

		t.Run("detects json fields", func(t *testing.T) {
			df := parseDetectedFields(uint32(15), logqlmodel.Streams([]push.Stream{nginxStream}))
			for _, expected := range []string{"host", "user_identifier", "datetime", "method", "request", "protocol", "status", "bytes", "referer"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1)
				require.Equal(t, "json", parsers[0])
			}

			// no parsers for structed metadata
			for _, expected := range []string{"detected_level"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 0)
			}
		})

		t.Run("detects mixed fields", func(t *testing.T) {
			df := parseDetectedFields(
				uint32(20),
				logqlmodel.Streams([]push.Stream{rulerStream, nginxStream}),
			)

			for _, expected := range []string{"ts", "caller", "tenant", "level", "duration", "msg"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1, "expected only logfmt parser for %s", expected)
				require.Equal(
					t,
					"logfmt",
					parsers[0],
					"expected only logfmt parser for %s",
					expected,
				)
			}

			for _, expected := range []string{"host", "user_identifier", "datetime", "request", "protocol", "status", "bytes", "referer"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1, "expected only json parser for %s", expected)
				require.Equal(t, "json", parsers[0], "expected only json parser for %s", expected)
			}

			// multiple parsers for fields that exist in both streams
			for _, expected := range []string{"method"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 2, "expected logfmt and json parser for %s", expected)
				require.Contains(t, parsers, "logfmt", "expected logfmt parser for %s", expected)
				require.Contains(t, parsers, "json", "expected json parser for %s", expected)
			}

			// no parsers for structed metadata
			for _, expected := range []string{"detected_level"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 0)
			}
		})

		t.Run("correctly applies _extracted for a single stream", func(t *testing.T) {
			rulerLbls := `{cluster="us-east-1", namespace="mimir-dev", pod="mimir-ruler-nfb37", service_name="mimir-ruler", tenant="42", caller="inside-the-house"}`
			rulerMetric, err := parser.ParseMetric(rulerLbls)
			require.NoError(t, err)

			rulerStream := push.Stream{
				Labels:  rulerLbls,
				Entries: rulerLines,
				Hash:    labels.StableHash(rulerMetric),
			}

			df := parseDetectedFields(uint32(15), logqlmodel.Streams([]push.Stream{rulerStream}))
			for _, expected := range []string{"ts", "caller_extracted", "tenant_extracted", "level", "method", "duration", "msg"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1)
				require.Equal(t, "logfmt", parsers[0])
			}

			// no parsers for structed metadata
			for _, expected := range []string{"detected_level"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 0)
			}
		})

		t.Run("correctly applies _extracted for multiple streams", func(t *testing.T) {
			rulerLbls := `{cluster="us-east-1", namespace="mimir-dev", pod="mimir-ruler-nfb37", service_name="mimir-ruler", tenant="42", caller="inside-the-house"}`
			rulerMetric, err := parser.ParseMetric(rulerLbls)
			require.NoError(t, err)

			rulerStream := push.Stream{
				Labels:  rulerLbls,
				Entries: rulerLines,
				Hash:    labels.StableHash(rulerMetric),
			}

			nginxLbls := `{ cluster="eu-west-1", level="debug", namespace="gateway", pod="nginx-json-oghco", service_name="nginx-json", host="localhost"}`
			nginxMetric, err := parser.ParseMetric(nginxLbls)
			require.NoError(t, err)

			nginxStream := push.Stream{
				Labels:  nginxLbls,
				Entries: nginxJSONLines,
				Hash:    labels.StableHash(nginxMetric),
			}

			df := parseDetectedFields(
				uint32(20),
				logqlmodel.Streams([]push.Stream{rulerStream, nginxStream}),
			)
			for _, expected := range []string{"ts", "caller_extracted", "tenant_extracted", "level", "duration", "msg"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1)
				require.Equal(t, "logfmt", parsers[0])
			}

			for _, expected := range []string{"host_extracted", "user_identifier", "datetime", "request", "protocol", "status", "bytes", "referer"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1, "expected only json parser for %s", expected)
				require.Equal(t, "json", parsers[0], "expected only json parser for %s", expected)
			}

			// multiple parsers for fields that exist in both streams
			for _, expected := range []string{"method"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 2, "expected logfmt and json parser for %s", expected)
				require.Contains(t, parsers, "logfmt", "expected logfmt parser for %s", expected)
				require.Contains(t, parsers, "json", "expected json parser for %s", expected)
			}

			// no parsers for structed metadata
			for _, expected := range []string{"detected_level"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 0)
			}
		})
	})

	t.Run("when parsers are supplied", func(t *testing.T) {
		infoDetectdFiledMetadata := []push.LabelAdapter{
			{
				Name:  "detected_level",
				Value: "info",
			},
		}

		parsedRulerFields := func(ts, tenant, duration string) []push.LabelAdapter {
			return []push.LabelAdapter{
				{
					Name:  "ts",
					Value: ts,
				},
				{
					Name:  "caller",
					Value: "grpc_logging.go:66",
				},
				{
					Name:  "tenant",
					Value: tenant,
				},
				{
					Name:  "level",
					Value: "info",
				},
				{
					Name:  "method",
					Value: "/cortex.Ingester/Push",
				},
				{
					Name:  "duration",
					Value: duration,
				},
				{
					Name:  "msg",
					Value: "gRPC",
				},
			}
		}

		rulerLbls := labels.FromStrings(
			"cluster", "us-east-1",
			"namespace", "mimir-dev",
			"pod", "mimir-ruler-nfb37",
			"service_name", "mimir-ruler",
		)

		rulerStreams := []push.Stream{}
		streamLbls := logql_log.NewBaseLabelsBuilder().ForLabels(rulerLbls, labels.StableHash(rulerLbls))

		for _, rulerFields := range [][]push.LabelAdapter{
			parsedRulerFields(
				"2024-09-05T15:36:38.757788067Z",
				"2419",
				"19.098s",
			),
			parsedRulerFields(
				"2024-09-05T15:36:38.698375619Z",
				"29",
				"5.471s",
			),
			parsedRulerFields(
				"2024-09-05T15:36:38.629424175Z",
				"2919",
				"29.234s",
			),
		} {
			streamLbls.Reset()

			var ts, tenant, duration push.LabelAdapter
			for _, field := range rulerFields {
				switch field.Name {
				case "ts":
					ts = field
				case "tenant":
					tenant = field
				case "duration":
					duration = field
				}

				streamLbls.Add(
					logql_log.ParsedLabel,
					labels.FromStrings(field.Name, field.Value),
				)
			}

			rulerStreams = append(rulerStreams, push.Stream{
				Labels: streamLbls.LabelsResult().String(),
				Entries: []push.Entry{
					{
						Timestamp: now,
						Line: fmt.Sprintf(
							"ts=%s caller=grpc_logging.go:66 tenant=%s level=info method=/cortex.Ingester/Push duration=%s msg=gRPC",
							ts.Value,
							tenant.Value,
							duration.Value,
						),
						StructuredMetadata: infoDetectdFiledMetadata,
						Parsed:             rulerFields,
					},
				},
			})
		}

		debugDetectedFieldMetadata := []push.LabelAdapter{
			{
				Name:  "detected_level",
				Value: "debug",
			},
		}

		parsedNginxFields := func(host, userIdentifier, datetime, method, request, protocol, status, bytes, referer string) []push.LabelAdapter {
			return []push.LabelAdapter{
				{
					Name:  "host",
					Value: host,
				},
				{
					Name:  "user_identifier",
					Value: userIdentifier,
				},
				{
					Name:  "datetime",
					Value: datetime,
				},
				{
					Name:  "method",
					Value: method,
				},
				{
					Name:  "request",
					Value: request,
				},
				{
					Name:  "protocol",
					Value: protocol,
				},
				{
					Name:  "status",
					Value: status,
				},
				{
					Name:  "bytes",
					Value: bytes,
				},
				{
					Name:  "referer",
					Value: referer,
				},
			}
		}

		nginxLbls := labels.FromStrings(
			"cluster", "eu-west-1",
			"level", "debug",
			"namespace", "gateway",
			"pod", "nginx-json-oghco",
			"service_name", "nginx-json",
		)

		nginxStreams := []push.Stream{}
		nginxStreamLbls := logql_log.NewBaseLabelsBuilder().ForLabels(nginxLbls, labels.StableHash(nginxLbls))

		for _, nginxFields := range [][]push.LabelAdapter{
			parsedNginxFields(
				"100.117.38.203",
				"nadre3722",
				"05/Sep/2024:16:13:56 +0000",
				"PATCH",
				"/api/loki/v1/push",
				"HTTP/2.0",
				"200",
				"9664",
				"https://www.seniorbleeding-edge.net/exploit/robust/whiteboard",
			),
			parsedNginxFields(
				"66.134.9.30",
				"-",
				"05/Sep/2024:16:13:55 +0000",
				"DELETE",
				"/api/mimir/v1/push",
				"HTTP/1.1",
				"200",
				"18688",
				"https://www.districtiterate.biz/synergistic/next-generation/extend",
			),
			parsedNginxFields(
				"66.134.9.30",
				"-",
				"05/Sep/2024:16:13:55 +0000",
				"GET",
				"/api/loki/v1/label/names",
				"HTTP/1.1",
				"200",
				"9314",
				"https://www.dynamicimplement.info/enterprise/distributed/incentivize/strategic",
			),
		} {
			nginxStreamLbls.Reset()

			var host, userIdentifier, datetime, method, request, protocol, status, bytes, referer push.LabelAdapter
			for _, field := range nginxFields {
				switch field.Name {
				case "host":
					host = field
				case "user_identifier":
					userIdentifier = field
				case "datetime":
					datetime = field
				case "method":
					method = field
				case "request":
					request = field
				case "protocol":
					protocol = field
				case "status":
					status = field
				case "bytes":
					bytes = field
				case "referer":
					referer = field
				}

				nginxStreamLbls.Add(
					logql_log.ParsedLabel,
					labels.FromStrings(field.Name, field.Value),
				)
			}

			nginxStreams = append(nginxStreams, push.Stream{
				Labels: nginxStreamLbls.LabelsResult().String(),
				Entries: []push.Entry{
					{
						Timestamp: now,
						Line: fmt.Sprintf(
							`{"host":"%s", "user-identifier":"%s", "datetime":"%s", "method": "%s", "request": "%s", "protocol":"%s", "status":%s, "bytes":%s, "referer": ":%s"}`,
							host.Value,
							userIdentifier.Value,
							datetime.Value,
							method.Value,
							request.Value,
							protocol.Value,
							status.Value,
							bytes.Value,
							referer.Value,
						),
						StructuredMetadata: debugDetectedFieldMetadata,
						Parsed:             nginxFields,
					},
				},
			})
		}

		t.Run("detect logfmt fields", func(t *testing.T) {
			df := parseDetectedFields(uint32(15), logqlmodel.Streams(rulerStreams))
			for _, expected := range []string{"ts", "caller", "tenant", "level", "method", "duration", "msg"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1)
				require.Equal(t, "logfmt", parsers[0])
			}

			// no parsers for structed metadata
			for _, expected := range []string{"detected_level"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 0)
			}
		})

		t.Run("detect json fields", func(t *testing.T) {
			df := parseDetectedFields(uint32(15), logqlmodel.Streams(nginxStreams))
			for _, expected := range []string{"host", "user_identifier", "datetime", "method", "request", "protocol", "status", "bytes", "referer"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1)
				require.Equal(t, "json", parsers[0])
			}

			// no parsers for structed metadata
			for _, expected := range []string{"detected_level"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 0)
			}
		})

		t.Run("detect mixed fields", func(t *testing.T) {
			streams := logqlmodel.Streams(rulerStreams)
			streams = append(streams, nginxStreams...)
			df := parseDetectedFields(
				uint32(20),
				streams,
			)

			for _, expected := range []string{"ts", "caller", "tenant", "level", "duration", "msg"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1, "expected only logfmt parser for %s", expected)
				require.Equal(
					t,
					"logfmt",
					parsers[0],
					"expected only logfmt parser for %s",
					expected,
				)
			}

			for _, expected := range []string{"host", "user_identifier", "datetime", "request", "protocol", "status", "bytes", "referer"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1, "expected only json parser for %s", expected)
				require.Equal(t, "json", parsers[0], "expected only json parser for %s", expected)
			}

			// multiple parsers for fields that exist in both streams
			for _, expected := range []string{"method"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 2, "expected logfmt and json parser for %s", expected)
				require.Contains(t, parsers, "logfmt", "expected logfmt parser for %s", expected)
				require.Contains(t, parsers, "json", "expected json parser for %s", expected)
			}

			// no parsers for structed metadata
			for _, expected := range []string{"detected_level"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 0)
			}
		})

		t.Run("correctly applies _extracted for a single stream", func(t *testing.T) {
			rulerLbls := `{cluster="us-east-1", namespace="mimir-dev", pod="mimir-ruler-nfb37", service_name="mimir-ruler", tenant="42", caller="inside-the-house"}`
			rulerMetric, err := parser.ParseMetric(rulerLbls)
			require.NoError(t, err)

			rulerStream := push.Stream{
				Labels: rulerLbls,
				Entries: []push.Entry{
					{
						Timestamp:          now,
						Line:               "ts=2024-09-05T15:36:38.757788067Z caller=grpc_logging.go:66 tenant=2419 level=info method=/cortex.Ingester/Push duration=19.098s msg=gRPC",
						StructuredMetadata: infoDetectdFiledMetadata,
						Parsed: []push.LabelAdapter{
							{
								Name:  "ts",
								Value: "2024-09-05T15:36:38.757788067Z",
							},
							{
								Name:  "caller_extracted",
								Value: "grpc_logging.go:66",
							},
							{
								Name:  "tenant_extracted",
								Value: "2419",
							},
							{
								Name:  "level",
								Value: "info",
							},
							{
								Name:  "method",
								Value: "/cortex.Ingester/Push",
							},
							{
								Name:  "duration",
								Value: "19.098s",
							},
							{
								Name:  "msg",
								Value: "gRPC",
							},
						},
					},
				},
				Hash: labels.StableHash(rulerMetric),
			}

			df := parseDetectedFields(uint32(15), logqlmodel.Streams([]push.Stream{rulerStream}))
			for _, expected := range []string{"ts", "caller_extracted", "tenant_extracted", "level", "method", "duration", "msg"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1)
				require.Equal(t, "logfmt", parsers[0])
			}

			// no parsers for structed metadata
			for _, expected := range []string{"detected_level"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 0)
			}
		})

		t.Run("correctly applies _extracted for multiple streams", func(t *testing.T) {
			rulerLbls := `{cluster="us-east-1", namespace="mimir-dev", pod="mimir-ruler-nfb37", service_name="mimir-ruler", tenant="42", caller="inside-the-house"}`
			rulerMetric, err := parser.ParseMetric(rulerLbls)
			require.NoError(t, err)

			rulerStream := push.Stream{
				Labels: rulerLbls,
				Entries: []push.Entry{
					{
						Timestamp:          now,
						Line:               "ts=2024-09-05T15:36:38.757788067Z caller=grpc_logging.go:66 tenant=2419 level=info method=/cortex.Ingester/Push duration=19.098s msg=gRPC",
						StructuredMetadata: infoDetectdFiledMetadata,
						Parsed: []push.LabelAdapter{
							{
								Name:  "ts",
								Value: "2024-09-05T15:36:38.757788067Z",
							},
							{
								Name:  "caller_extracted",
								Value: "grpc_logging.go:66",
							},
							{
								Name:  "tenant_extracted",
								Value: "2419",
							},
							{
								Name:  "level",
								Value: "info",
							},
							{
								Name:  "method",
								Value: "/cortex.Ingester/Push",
							},
							{
								Name:  "duration",
								Value: "19.098s",
							},
							{
								Name:  "msg",
								Value: "gRPC",
							},
						},
					},
				},
				Hash: labels.StableHash(rulerMetric),
			}

			nginxLbls := `{ cluster="eu-west-1", level="debug", namespace="gateway", pod="nginx-json-oghco", service_name="nginx-json", host="localhost"}`
			nginxMetric, err := parser.ParseMetric(nginxLbls)
			require.NoError(t, err)

			nginxStream := push.Stream{
				Labels: nginxLbls,
				Entries: []push.Entry{
					{
						Timestamp:          now,
						Line:               `{"host":"100.117.38.203", "user-identifier":"nader3722", "datetime":"05/Sep/2024:16:13:56 +0000", "method": "PATCH", "request": "/api/loki/v1/push", "protocol":"HTTP/2.0", "status":200, "bytes":9664, "referer": "https://www.seniorbleeding-edge.net/exploit/robust/whiteboard"}`,
						StructuredMetadata: debugDetectedFieldMetadata,
						Parsed: []push.LabelAdapter{
							{
								Name:  "host_extracted",
								Value: "100.117.38.203",
							},
							{
								Name:  "user_identifier",
								Value: "nader3722",
							},
							{
								Name:  "datetime",
								Value: "05/Sep/2024:16:13:56 +0000",
							},
							{
								Name:  "method",
								Value: "PATCH",
							},
							{
								Name:  "request",
								Value: "/api/loki/v1/push",
							},
							{
								Name:  "protocol",
								Value: "HTTP/2.0",
							},
							{
								Name:  "status",
								Value: "200",
							},
							{
								Name:  "bytes",
								Value: "9664",
							},
							{
								Name:  "referer",
								Value: "https://www.seniorbleeding-edge.net/exploit/robust/whiteboard",
							},
						},
					},
				},
				Hash: labels.StableHash(nginxMetric),
			}

			df := parseDetectedFields(
				uint32(20),
				logqlmodel.Streams([]push.Stream{rulerStream, nginxStream}),
			)
			for _, expected := range []string{"ts", "caller_extracted", "tenant_extracted", "level", "duration", "msg"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1)
				require.Equal(t, "logfmt", parsers[0])
			}

			for _, expected := range []string{"host_extracted", "user_identifier", "datetime", "request", "protocol", "status", "bytes", "referer"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 1, "expected only json parser for %s", expected)
				require.Equal(t, "json", parsers[0], "expected only json parser for %s", expected)
			}

			// multiple parsers for fields that exist in both streams
			for _, expected := range []string{"method"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 2, "expected logfmt and json parser for %s", expected)
				require.Contains(t, parsers, "logfmt", "expected logfmt parser for %s", expected)
				require.Contains(t, parsers, "json", "expected json parser for %s", expected)
			}

			// no parsers for structed metadata
			for _, expected := range []string{"detected_level"} {
				require.Contains(t, df, expected)
				parsers := df[expected].parsers

				require.Len(t, parsers, 0)
			}
		})
	})

	t.Run("handles level in all the places", func(t *testing.T) {
		rulerLbls := `{cluster="us-east-1", namespace="mimir-dev", pod="mimir-ruler-nfb37", service_name="mimir-ruler", tenant="42", caller="inside-the-house", level="debug"}`
		rulerMetric, err := parser.ParseMetric(rulerLbls)
		require.NoError(t, err)

		rulerStream := push.Stream{
			Labels: rulerLbls,
			Entries: []push.Entry{
				{
					Timestamp: now,
					Line:      "ts=2024-09-05T15:36:38.757788067Z caller=grpc_logging.go:66 tenant=2419 level=info method=/cortex.Ingester/Push duration=19.098s msg=gRPC",
					StructuredMetadata: []push.LabelAdapter{
						{
							Name:  "detected_level",
							Value: "debug",
						},
					},
					Parsed: []push.LabelAdapter{
						{
							Name:  "level_extracted",
							Value: "info",
						},
					},
				},
			},
			Hash: labels.StableHash(rulerMetric),
		}

		df := parseDetectedFields(
			uint32(20),
			logqlmodel.Streams([]push.Stream{rulerStream, rulerStream}),
		)

		detectedLevelField := df["detected_level"]
		require.Len(t, detectedLevelField.parsers, 0)
		require.Equal(t, uint64(1), detectedLevelField.sketch.Estimate())

		levelField := df["level_extracted"]
		require.Len(t, levelField.parsers, 1)
		require.Contains(t, levelField.parsers, "logfmt")
		require.Equal(t, uint64(1), levelField.sketch.Estimate())
	})
}

func mockLogfmtStreamWithLabels(_ int, quantity int, lbls string) logproto.Stream {
	entries := make([]logproto.Entry, 0, quantity)
	streamLabels, err := syntax.ParseLabels(lbls)
	if err != nil {
		streamLabels = labels.EmptyLabels()
	}

	lblBuilder := logql_log.NewBaseLabelsBuilder().ForLabels(streamLabels, labels.StableHash(streamLabels))
	logFmtParser := logql_log.NewLogfmtParser(false, false)

	// used for detected fields queries which are always BACKWARD
	for i := quantity; i > 0; i-- {
		line := fmt.Sprintf(
			`message="line %d" count=%d fake=true bytes=%dMB duration=%dms percent=%f even=%t name=bar`,
			i,
			i,
			(i * 10),
			(i * 256),
			float32(i*10.0),
			(i%2 == 0),
		)

		entry := logproto.Entry{
			Timestamp: time.Unix(int64(i), 0),
			Line:      line,
		}
		_, logfmtSuccess := logFmtParser.Process(0, []byte(line), lblBuilder)
		if logfmtSuccess {
			entry.Parsed = logproto.FromLabelsToLabelAdapters(lblBuilder.LabelsResult().Parsed())
		}
		entries = append(entries, entry)
	}

	return logproto.Stream{
		Entries: entries,
		Labels:  lbls,
	}
}

func mockLogfmtStreamWithLabelsAndStructuredMetadata(
	from int,
	quantity int,
	lbls string,
) logproto.Stream {
	var entries []logproto.Entry
	metadata := push.LabelsAdapter{
		{
			Name:  "constant",
			Value: "constant",
		},
	}

	for i := from; i < from+quantity; i++ {
		metadata = append(metadata, push.LabelAdapter{
			Name:  "variable",
			Value: fmt.Sprintf("value%d", i),
		})
	}

	streamLabels, err := syntax.ParseLabels(lbls)
	if err != nil {
		streamLabels = labels.EmptyLabels()
	}

	lblBuilder := logql_log.NewBaseLabelsBuilder().ForLabels(streamLabels, labels.StableHash(streamLabels))
	logFmtParser := logql_log.NewLogfmtParser(false, false)

	// used for detected fields queries which are always BACKWARD
	for i := quantity; i > 0; i-- {
		line := fmt.Sprintf(
			`message="line %d" count=%d fake=true bytes=%dMB duration=%dms percent=%f even=%t name=bar`,
			i,
			i,
			(i * 10),
			(i * 256),
			float32(i*10.0),
			(i%2 == 0),
		)

		entry := logproto.Entry{
			Timestamp:          time.Unix(int64(i), 0),
			Line:               line,
			StructuredMetadata: metadata,
		}
		_, logfmtSuccess := logFmtParser.Process(0, []byte(line), lblBuilder)
		if logfmtSuccess {
			entry.Parsed = logproto.FromLabelsToLabelAdapters(lblBuilder.LabelsResult().Parsed())
		}
		entries = append(entries, entry)
	}

	return logproto.Stream{
		Labels:  lbls,
		Entries: entries,
	}
}

func limitedHandler(stream logproto.Stream) base.Handler {
	return base.HandlerFunc(
		func(_ context.Context, _ base.Request) (base.Response, error) {
			return &LokiResponse{
				Status: "success",
				Data: LokiData{
					ResultType: loghttp.ResultTypeStream,
					Result: []logproto.Stream{
						stream,
					},
				},
				Direction: logproto.BACKWARD,
			}, nil
		})
}

func logHandler(stream logproto.Stream) base.Handler {
	return base.HandlerFunc(
		func(_ context.Context, _ base.Request) (base.Response, error) {
			return &LokiResponse{
				Status: "success",
				Data: LokiData{
					ResultType: loghttp.ResultTypeStream,
					Result: []logproto.Stream{
						stream,
					},
				},
				Direction: logproto.BACKWARD,
			}, nil
		})
}

// TODO(twhitney): Is this releated to the now deprecated Querier endpoint?
func TestQuerier_DetectedFields(t *testing.T) {
	limits := fakeLimits{
		maxSeries:               math.MaxInt32,
		maxQueryParallelism:     1,
		tsdbMaxQueryParallelism: 1,
		maxQueryBytesRead:       1000,
		maxQuerierBytesRead:     100,
	}

	request := DetectedFieldsRequest{
		logproto.DetectedFieldsRequest{
			Start:     time.Now().Add(-1 * time.Minute),
			End:       time.Now(),
			Query:     `{type="test"} | logfmt | json`,
			LineLimit: 1000,
			Limit:     1000,
		},
		"/loki/api/v1/detected_fields",
	}

	handleRequest := func(handler base.Handler, request DetectedFieldsRequest) *logproto.DetectedFieldsResponse {
		ctx := context.Background()
		ctx = user.InjectOrgID(ctx, "test-tenant")

		resp, err := handler.Do(ctx, &request)
		require.NoError(t, err)

		r, ok := resp.(*DetectedFieldsResponse)
		require.True(t, ok)

		return r.Response
	}

	t.Run("returns detected fields from queried logs", func(t *testing.T) {
		handler := NewDetectedFieldsHandler(
			limitedHandler(mockLogfmtStreamWithLabels(1, 5, `{type="test", name="foo"}`)),
			logHandler(mockLogfmtStreamWithLabels(1, 5, `{type="test", name="foo"}`)),
			limits,
		)

		detectedFields := handleRequest(handler, request).Fields
		// log lines come from querier_mock_test.go
		// message="line %d" count=%d fake=true bytes=%dMB duration=%dms percent=%f even=%t
		assert.Len(t, detectedFields, 8)
		expectedCardinality := map[string]uint64{
			"message":        5,
			"count":          5,
			"fake":           1,
			"bytes":          5,
			"duration":       5,
			"percent":        5,
			"even":           2,
			"name_extracted": 1,
		}
		for _, d := range detectedFields {
			card := expectedCardinality[d.Label]
			assert.Equal(t, card, d.Cardinality, "Expected cardinality mismatch for: %s", d.Label)
		}
	})

	t.Run("returns detected fields with structured metadata from queried logs", func(t *testing.T) {
		handler := NewDetectedFieldsHandler(
			limitedHandler(
				mockLogfmtStreamWithLabelsAndStructuredMetadata(1, 5, `{type="test", name="bob"}`),
			),
			logHandler(
				mockLogfmtStreamWithLabelsAndStructuredMetadata(1, 5, `{type="test", name="bob"}`),
			),
			limits,
		)

		detectedFields := handleRequest(handler, request).Fields
		// log lines come from querier_mock_test.go
		// message="line %d" count=%d fake=true bytes=%dMB duration=%dms percent=%f even=%t
		assert.Len(t, detectedFields, 10)
		expectedCardinality := map[string]uint64{
			"variable":       5,
			"constant":       1,
			"message":        5,
			"count":          5,
			"fake":           1,
			"bytes":          5,
			"duration":       5,
			"percent":        5,
			"even":           2,
			"name_extracted": 1,
		}
		for _, d := range detectedFields {
			card := expectedCardinality[d.Label]
			assert.Equal(t, card, d.Cardinality, "Expected cardinality mismatch for: %s", d.Label)
		}
	})

	t.Run("correctly identifies different field types", func(t *testing.T) {
		handler := NewDetectedFieldsHandler(
			limitedHandler(mockLogfmtStreamWithLabels(1, 2, `{type="test", name="foo"}`)),
			logHandler(mockLogfmtStreamWithLabels(1, 2, `{type="test", name="foo"}`)),
			limits,
		)

		detectedFields := handleRequest(handler, request).Fields
		// log lines come from querier_mock_test.go
		// message="line %d" count=%d fake=true bytes=%dMB duration=%dms percent=%f even=%t
		assert.Len(t, detectedFields, 8)

		var messageField, countField, bytesField, durationField, floatField, evenField *logproto.DetectedField
		for _, field := range detectedFields {
			print(field.Label)
			switch field.Label {
			case "message":
				messageField = field
			case "count":
				countField = field
			case "bytes":
				bytesField = field
			case "duration":
				durationField = field
			case "percent":
				floatField = field
			case "even":
				evenField = field
			}
		}

		assert.Equal(t, logproto.DetectedFieldString, messageField.Type)
		assert.Equal(t, logproto.DetectedFieldInt, countField.Type)
		assert.Equal(t, logproto.DetectedFieldBytes, bytesField.Type)
		assert.Equal(t, logproto.DetectedFieldDuration, durationField.Type)
		assert.Equal(t, logproto.DetectedFieldFloat, floatField.Type)
		assert.Equal(t, logproto.DetectedFieldBoolean, evenField.Type)
	})

	t.Run(
		"correctly identifies parser to use with logfmt and structured metadata",
		func(t *testing.T) {
			handler := NewDetectedFieldsHandler(
				limitedHandler(
					mockLogfmtStreamWithLabelsAndStructuredMetadata(1, 2, `{type="test"}`),
				),
				logHandler(mockLogfmtStreamWithLabelsAndStructuredMetadata(1, 2, `{type="test"}`)),
				limits,
			)

			detectedFields := handleRequest(handler, request).Fields
			// log lines come from querier_mock_test.go
			// message="line %d" count=%d fake=true bytes=%dMB duration=%dms percent=%f even=%t
			assert.Len(t, detectedFields, 10)

			var messageField, countField, bytesField, durationField, floatField, evenField, constantField, variableField *logproto.DetectedField
			for _, field := range detectedFields {
				switch field.Label {
				case "message":
					messageField = field
				case "count":
					countField = field
				case "bytes":
					bytesField = field
				case "duration":
					durationField = field
				case "percent":
					floatField = field
				case "even":
					evenField = field
				case "constant":
					constantField = field
				case "variable":
					variableField = field
				}
			}

			assert.Equal(t, []string{"logfmt"}, messageField.Parsers)
			assert.Equal(t, []string{"logfmt"}, countField.Parsers)
			assert.Equal(t, []string{"logfmt"}, bytesField.Parsers)
			assert.Equal(t, []string{"logfmt"}, durationField.Parsers)
			assert.Equal(t, []string{"logfmt"}, floatField.Parsers)
			assert.Equal(t, []string{"logfmt"}, evenField.Parsers)
			assert.Equal(t, []string(nil), constantField.Parsers)
			assert.Equal(t, []string(nil), variableField.Parsers)
		},
	)

	t.Run(
		"adds _extracted suffix to detected fields that conflict with indexed labels",
		func(t *testing.T) {
			handler := NewDetectedFieldsHandler(
				limitedHandler(
					mockLogfmtStreamWithLabelsAndStructuredMetadata(
						1,
						2,
						`{type="test", name="bob"}`,
					),
				),
				logHandler(
					mockLogfmtStreamWithLabelsAndStructuredMetadata(
						1,
						2,
						`{type="test", name="bob"}`,
					),
				),
				limits,
			)

			detectedFields := handleRequest(handler, request).Fields
			// log lines come from querier_mock_test.go
			// message="line %d" count=%d fake=true bytes=%dMB duration=%dms percent=%f even=%t
			assert.Len(t, detectedFields, 10)

			var nameField *logproto.DetectedField
			for _, field := range detectedFields {
				switch field.Label {
				case "name_extracted":
					nameField = field
				}
			}

			assert.NotNil(t, nameField)
			assert.Equal(t, "name_extracted", nameField.Label)
			assert.Equal(t, logproto.DetectedFieldString, nameField.Type)
			assert.Equal(t, []string{"logfmt"}, nameField.Parsers)
			assert.Equal(t, uint64(1), nameField.Cardinality)
		},
	)

	t.Run("returns values for a detected fields", func(t *testing.T) {
		handler := NewDetectedFieldsHandler(
			limitedHandler(
				mockLogfmtStreamWithLabelsAndStructuredMetadata(1, 5, `{type="test", name="bob"}`),
			),
			logHandler(
				mockLogfmtStreamWithLabelsAndStructuredMetadata(1, 5, `{type="test", name="bob"}`),
			),
			limits,
		)

		request := DetectedFieldsRequest{
			logproto.DetectedFieldsRequest{
				Start:     time.Now().Add(-1 * time.Minute),
				End:       time.Now(),
				Query:     `{type="test"} | logfmt | json`,
				LineLimit: 1000,
				Limit:     1000,
				Values:    true,
				Name:      "message",
			},
			"/loki/api/v1/detected_field/message/values",
		}

		detectedFieldValues := handleRequest(handler, request).Values
		// log lines come from querier_mock_test.go
		// message="line %d" count=%d fake=true bytes=%dMB duration=%dms percent=%f even=%t
		assert.Len(t, detectedFieldValues, 5)

		slices.Sort(detectedFieldValues)
		assert.Equal(t, []string{
			"line 1",
			"line 2",
			"line 3",
			"line 4",
			"line 5",
		}, detectedFieldValues)
	})

	t.Run(
		"returns values for a detected fields, enforcing the limit and removing duplicates",
		func(t *testing.T) {
			handler := NewDetectedFieldsHandler(
				limitedHandler(
					mockLogfmtStreamWithLabelsAndStructuredMetadata(
						1,
						5,
						`{type="test"}`,
					),
				),
				logHandler(
					mockLogfmtStreamWithLabelsAndStructuredMetadata(
						1,
						5,
						`{type="test"}`,
					),
				),
				limits,
			)

			request := DetectedFieldsRequest{
				logproto.DetectedFieldsRequest{
					Start:     time.Now().Add(-1 * time.Minute),
					End:       time.Now(),
					Query:     `{type="test"} | logfmt | json`,
					LineLimit: 1000,
					Limit:     3,
					Values:    true,
					Name:      "message",
				},
				"/loki/api/v1/detected_field/message/values",
			}

			detectedFieldValues := handleRequest(handler, request).Values
			// log lines come from querier_mock_test.go
			// message="line %d" count=%d fake=true bytes=%dMB duration=%dms percent=%f even=%t
			assert.Len(t, detectedFieldValues, 3)

			request = DetectedFieldsRequest{
				logproto.DetectedFieldsRequest{
					Start:     time.Now().Add(-1 * time.Minute),
					End:       time.Now(),
					Query:     `{type="test"} | logfmt | json`,
					LineLimit: 1000,
					Limit:     3,
					Values:    true,
					Name:      "name",
				},
				"/loki/api/v1/detected_field/name/values",
			}

			secondValues := handleRequest(handler, request).Values
			// log lines come from querier_mock_test.go
			// message="line %d" count=%d fake=true bytes=%dMB duration=%dms percent=%f even=%t name=bar
			assert.Len(t, secondValues, 1)

			assert.Equal(t, []string{
				"bar",
			}, secondValues)
		},
	)

	t.Run("correctly formats bytes values for detected fields", func(t *testing.T) {
		lbls := `{cluster="us-east-1", namespace="mimir-dev", pod="mimir-ruler-nfb37", service_name="mimir-ruler"}`
		metric, err := parser.ParseMetric(lbls)
		require.NoError(t, err)
		now := time.Now()

		infoDetectdFiledMetadata := []push.LabelAdapter{
			{
				Name:  "detected_level",
				Value: "info",
			},
		}

		lines := []push.Entry{
			{
				Timestamp:          now,
				Line:               "ts=2024-09-05T15:36:38.757788067Z caller=metrics.go:66 tenant=2419 level=info bytes=1024",
				StructuredMetadata: infoDetectdFiledMetadata,
			},
			{
				Timestamp:          now,
				Line:               `ts=2024-09-05T15:36:38.698375619Z caller=grpc_logging.go:66 tenant=29 level=info bytes="1024 MB"`,
				StructuredMetadata: infoDetectdFiledMetadata,
			},
			{
				Timestamp:          now,
				Line:               "ts=2024-09-05T15:36:38.629424175Z caller=grpc_logging.go:66 tenant=2919 level=info bytes=1024KB",
				StructuredMetadata: infoDetectdFiledMetadata,
			},
		}
		stream := push.Stream{
			Labels:  lbls,
			Entries: lines,
			Hash:    labels.StableHash(metric),
		}

		handler := NewDetectedFieldsHandler(
			limitedHandler(stream),
			logHandler(stream),
			limits,
		)

		request := DetectedFieldsRequest{
			logproto.DetectedFieldsRequest{
				Start:     time.Now().Add(-1 * time.Minute),
				End:       time.Now(),
				Query:     `{cluster="us-east-1"} | logfmt`,
				LineLimit: 1000,
				Limit:     3,
				Values:    true,
				Name:      "bytes",
			},
			"/loki/api/v1/detected_field/bytes/values",
		}

		detectedFieldValues := handleRequest(handler, request).Values
		slices.Sort(detectedFieldValues)
		require.Equal(t, []string{
			"1.0GB",
			"1.0MB",
			"1024",
		}, detectedFieldValues)

		// does not affect other numeric values
		request = DetectedFieldsRequest{
			logproto.DetectedFieldsRequest{
				Start:     time.Now().Add(-1 * time.Minute),
				End:       time.Now(),
				Query:     `{cluster="us-east-1"} | logfmt`,
				LineLimit: 1000,
				Limit:     3,
				Values:    true,
				Name:      "tenant",
			},
			"/loki/api/v1/detected_field/tenant/values",
		}

		detectedFieldValues = handleRequest(handler, request).Values
		slices.Sort(detectedFieldValues)
		require.Equal(t, []string{
			"2419",
			"29",
			"2919",
		}, detectedFieldValues)
	})
}

func BenchmarkQuerierDetectedFields(b *testing.B) {
	limits := fakeLimits{
		maxSeries:               math.MaxInt32,
		maxQueryParallelism:     1,
		tsdbMaxQueryParallelism: 1,
		maxQueryBytesRead:       1000,
		maxQuerierBytesRead:     100,
	}

	request := logproto.DetectedFieldsRequest{
		Start:     time.Now().Add(-1 * time.Minute),
		End:       time.Now(),
		Query:     `{type="test"}`,
		LineLimit: 1000,
		Limit:     1000,
	}

	b.ReportAllocs()
	b.ResetTimer()

	handler := NewDetectedFieldsHandler(
		limitedHandler(
			mockLogfmtStreamWithLabelsAndStructuredMetadata(1, 5, `{type="test", name="bob"}`),
		),
		logHandler(
			mockLogfmtStreamWithLabelsAndStructuredMetadata(1, 5, `{type="test", name="bob"}`),
		),
		limits,
	)

	for i := 0; i < b.N; i++ {
		ctx := context.Background()
		ctx = user.InjectOrgID(ctx, "test-tenant")

		resp, err := handler.Do(ctx, &request)
		assert.NoError(b, err)

		_, ok := resp.(*DetectedFieldsResponse)
		require.True(b, ok)
	}
}

func TestNestedJSONFieldDetection(t *testing.T) {
	t.Run("correctly detects nested JSON fields", func(t *testing.T) {
		now := time.Now()

		nestedJSONLines := []push.Entry{
			{
				Timestamp: now,
				Line: `{
        "user":{
          "id":123,
          "name":"alice",
          "settings":{
            "theme":"dark",
            "notifications":true
          }
        },
        "app":{
          "version":"1.0",
          "metrics":{
            "cpu":45.6,
            "memory":"1.5GB"
          }
        }
      }`,
				StructuredMetadata: []push.LabelAdapter{},
			},
			{
				Timestamp: now,
				Line: `{
        "user":{
          "id":456,
          "name":"bob",
          "settings":{
            "theme":"light",
            "notifications":false
          }
        },
        "app":{
          "version":"1.0",
          "metrics":{
            "cpu":32.1,
            "memory":"2.0GB"
          }
        }
      }`,
				StructuredMetadata: []push.LabelAdapter{},
			},
		}

		nestedJSONLbls := `{cluster="test-cluster", job="json-test"}`
		nestedJSONMetric, err := parser.ParseMetric(nestedJSONLbls)
		require.NoError(t, err)

		nestedJSONStream := push.Stream{
			Labels:  nestedJSONLbls,
			Entries: nestedJSONLines,
			Hash:    labels.StableHash(nestedJSONMetric),
		}

		df := parseDetectedFields(uint32(20), logqlmodel.Streams([]push.Stream{nestedJSONStream}))

		// Test for nested fields
		expectedNestedFieldTypes := map[string]logproto.DetectedFieldType{
			"user_id":                     logproto.DetectedFieldInt,
			"user_name":                   logproto.DetectedFieldString,
			"user_settings_theme":         logproto.DetectedFieldString,
			"user_settings_notifications": logproto.DetectedFieldBoolean,
			"app_version":                 logproto.DetectedFieldFloat,
			"app_metrics_cpu":             logproto.DetectedFieldFloat,
			"app_metrics_memory":          logproto.DetectedFieldBytes,
		}

		expectFieldsToPaths := map[string][]string{
			"user_id":                     {"user", "id"},
			"user_name":                   {"user", "name"},
			"user_settings_theme":         {"user", "settings", "theme"},
			"user_settings_notifications": {"user", "settings", "notifications"},
			"app_version":                 {"app", "version"},
			"app_metrics_cpu":             {"app", "metrics", "cpu"},
			"app_metrics_memory":          {"app", "metrics", "memory"},
		}

		for field, expectedType := range expectedNestedFieldTypes {
			require.Contains(t, df, field, "Missing expected nested field: %s", field)
			require.Equal(t, expectedType, df[field].fieldType, "Wrong type for field %s", field)
		}

		for field, expectedPath := range expectFieldsToPaths {
			require.Contains(t, df, field, "Missing expected nested field: %s", field)
			require.Equal(t, expectedPath, df[field].jsonPath, "Wrong json path for field %s", field)
		}
	})

	t.Run("correctly detects sanitized JSON fields, including difficult keys", func(t *testing.T) {
		now := time.Now()

		nestedJSONLines := []push.Entry{
			{
				Timestamp: now,
				Line: `{
        "user":{
          "id":123,
          "name":"alice",
          "settings":{
            "theme":"dark",
            "notifications":true,
          }
        },
        "app-id": "abc",
			  "app_name": "foo",
        "app": {
          "terrible/key/name": "four",
        },
        "other.bad.key.name": "three",
        "key with spaces": "space",
        "nested key with spaces": {
          "nest": "thermostat",
        }
      }`,
				StructuredMetadata: []push.LabelAdapter{},
			},
			{
				Timestamp: now,
				Line: `{
        "user":{
          "id":456,
          "name":"bob",
          "settings":{
            "theme":"light",
            "notifications":false
          },
        },
        "app-id": "xyz",
			  "app_name": "bar",
        "app": {
          "terrible/key/name": "four",
        },
        "other.bad.key.name": "five",
        "key with spaces": "blank",
        "nested key with spaces": {
          "nest": "protect",
        }
      }`,
				StructuredMetadata: []push.LabelAdapter{},
			},
		}

		nestedJSONLbls := `{cluster="test-cluster", job="json-test"}`
		nestedJSONMetric, err := parser.ParseMetric(nestedJSONLbls)
		require.NoError(t, err)

		nestedJSONStream := push.Stream{
			Labels:  nestedJSONLbls,
			Entries: nestedJSONLines,
			Hash:    labels.StableHash(nestedJSONMetric),
		}

		df := parseDetectedFields(uint32(20), logqlmodel.Streams([]push.Stream{nestedJSONStream}))

		expectFieldsToPaths := map[string][]string{
			"user_id":                     {"user", "id"},
			"user_name":                   {"user", "name"},
			"user_settings_theme":         {"user", "settings", "theme"},
			"user_settings_notifications": {"user", "settings", "notifications"},
			"app_id":                      {"app-id"},
			"app_name":                    {"app_name"},
			"app_terrible_key_name":       {"app", "terrible/key/name"},
			"other_bad_key_name":          {"other.bad.key.name"},
			"key_with_spaces":             {"key with spaces"},
			"nested_key_with_spaces_nest": {"nested key with spaces", "nest"},
		}

		for field, expectedPath := range expectFieldsToPaths {
			require.Contains(t, df, field, "Missing expected nested field: %s", field)
			require.Equal(t, expectedPath, df[field].jsonPath, "Wrong json path for field %s", field)
		}
	})
}
