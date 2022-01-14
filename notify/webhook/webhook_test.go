// Copyright 2019 Prometheus Team
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package webhook

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/go-kit/log"
	commoncfg "github.com/prometheus/common/config"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/prometheus/alertmanager/config"
	"github.com/prometheus/alertmanager/notify"
	"github.com/prometheus/alertmanager/notify/test"
	"github.com/prometheus/alertmanager/template"
	"github.com/prometheus/alertmanager/types"

	jsonnet "github.com/google/go-jsonnet"
)

func TestWebhookRetry(t *testing.T) {
	u, err := url.Parse("http://example.com")
	if err != nil {
		require.NoError(t, err)
	}
	notifier, err := New(
		&config.WebhookConfig{
			URL:        &config.URL{URL: u},
			HTTPConfig: &commoncfg.HTTPClientConfig{},
		},
		test.CreateTmpl(t),
		log.NewNopLogger(),
	)
	if err != nil {
		require.NoError(t, err)
	}
	for statusCode, expected := range test.RetryTests(test.DefaultRetryCodes()) {
		actual, _ := notifier.retrier.Check(statusCode, nil)
		require.Equal(t, expected, actual, fmt.Sprintf("error on status %d", statusCode))
	}
}

func TestWebhookTruncateAlerts(t *testing.T) {
	alerts := make([]*types.Alert, 10)

	truncatedAlerts, numTruncated := truncateAlerts(0, alerts)
	require.Len(t, truncatedAlerts, 10)
	require.EqualValues(t, numTruncated, 0)

	truncatedAlerts, numTruncated = truncateAlerts(4, alerts)
	require.Len(t, truncatedAlerts, 4)
	require.EqualValues(t, numTruncated, 6)

	truncatedAlerts, numTruncated = truncateAlerts(100, alerts)
	require.Len(t, truncatedAlerts, 10)
	require.EqualValues(t, numTruncated, 0)
}

func TestWebhookEvalJsonnetTemplate(t *testing.T) {
	u, err := url.Parse("http://example.com/")
	require.NoError(t, err)

	ctx := context.Background()
	tmpl := &template.Template{ExternalURL: u}
	startTime := time.Time{}.Add(1 * time.Second)
	vm := jsonnet.MakeVM()
	for _, tc := range []struct {
		alerts         []*types.Alert
		name           string
		templateString string
		exp            map[string]string
	}{
		{
			name: "test1",
			alerts: []*types.Alert{
				{
					Alert: model.Alert{
						StartsAt: startTime,
						Labels: model.LabelSet{
							model.LabelName("severity"): model.LabelValue("warning"),
							model.LabelName("job"):      model.LabelValue("foo"),
						},
						Annotations: model.LabelSet{
							model.LabelName("description"): model.LabelValue("something happened"),
							model.LabelName("runbook"):     model.LabelValue("foo"),
						},
					},
				},
			},
			templateString: `local data = import "data.libsonnet";
			{numAlerts: std.length(data["alerts"])}`,
			exp: map[string]string{
				"numAlerts": "1\n",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			templateData := notify.GetTemplateData(ctx, tmpl, tc.alerts, log.NewNopLogger())
			ast, err := jsonnet.SnippetToAST("foo", tc.templateString)
			if err != nil {
				require.NoError(t, err)
			}
			evaluated, err := evalJsonnetTemplate(vm, &ast, templateData)
			require.NoError(t, err)
			require.Equal(t, tc.exp, evaluated)
		})
	}
}

func BenchmarkWebhookEval(b *testing.B) {
	b.ReportAllocs()
	u, _ := url.Parse("http://example.com/")
	ctx := context.Background()
	tmpl := &template.Template{ExternalURL: u}
	startTime := time.Time{}.Add(1 * time.Second)
	alerts := []*types.Alert{
		{
			Alert: model.Alert{
				StartsAt: startTime,
				Labels: model.LabelSet{
					model.LabelName("severity"): model.LabelValue("warning"),
					model.LabelName("job"):      model.LabelValue("foo"),
				},
				Annotations: model.LabelSet{
					model.LabelName("description"): model.LabelValue("something happened"),
					model.LabelName("runbook"):     model.LabelValue("foo"),
				},
			},
		},
	}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		generateMessage(ctx, tmpl, log.NewNopLogger(), 100, nil, nil, alerts)
	}
}

func BenchmarkWebhookEvalJsonnet(b *testing.B) {
	b.ReportAllocs()
	u, _ := url.Parse("http://example.com/")
	ctx := context.Background()
	tmpl := &template.Template{ExternalURL: u}
	startTime := time.Time{}.Add(1 * time.Second)
	alerts := []*types.Alert{
		{
			Alert: model.Alert{
				StartsAt: startTime,
				Labels: model.LabelSet{
					model.LabelName("severity"): model.LabelValue("warning"),
					model.LabelName("job"):      model.LabelValue("foo"),
				},
				Annotations: model.LabelSet{
					model.LabelName("description"): model.LabelValue("something happened"),
					model.LabelName("runbook"):     model.LabelValue("foo"),
				},
			},
		},
	}
	templateString := `local data = import "data.libsonnet";
			{numAlerts: std.length(data["alerts"])}`
	ast, err := jsonnet.SnippetToAST("foo", templateString)
	if err != nil {
		require.NoError(b, err)
	}
	vm := jsonnet.MakeVM()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		generateMessage(ctx, tmpl, log.NewNopLogger(), 100, vm, &ast, alerts)
	}
}
