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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	commoncfg "github.com/prometheus/common/config"

	"github.com/prometheus/alertmanager/config"
	"github.com/prometheus/alertmanager/notify"
	"github.com/prometheus/alertmanager/template"
	"github.com/prometheus/alertmanager/types"

	jsonnet "github.com/google/go-jsonnet"
)

// Notifier implements a Notifier for generic webhooks.
type Notifier struct {
	conf    *config.WebhookConfig
	tmpl    *template.Template
	logger  log.Logger
	client  *http.Client
	retrier *notify.Retrier
	jsonnet *jsonnet.VM
}

// New returns a new Webhook.
func New(conf *config.WebhookConfig, t *template.Template, l log.Logger, httpOpts ...commoncfg.HTTPClientOption) (*Notifier, error) {
	client, err := commoncfg.NewClientFromConfig(*conf.HTTPConfig, "webhook", httpOpts...)
	if err != nil {
		return nil, err
	}

	var vm *jsonnet.VM
	if conf.MessageTemplate != "" {
		vm = jsonnet.MakeVM()
	}
	return &Notifier{
		conf:   conf,
		tmpl:   t,
		logger: l,
		client: client,
		// Webhooks are assumed to respond with 2xx response codes on a successful
		// request and 5xx response codes are assumed to be recoverable.
		retrier: &notify.Retrier{
			CustomDetailsFunc: func(int, io.Reader) string {
				return conf.URL.String()
			},
		},
		jsonnet: vm,
	}, nil
}

// Message defines the JSON object send to webhook endpoints.
type Message struct {
	*template.Data

	// The protocol version.
	Version         string            `json:"version"`
	GroupKey        string            `json:"groupKey"`
	TruncatedAlerts uint64            `json:"truncatedAlerts"`
	Message         map[string]string `json:"message,omitempty"`
}

func truncateAlerts(maxAlerts uint64, alerts []*types.Alert) ([]*types.Alert, uint64) {
	if maxAlerts != 0 && uint64(len(alerts)) > maxAlerts {
		return alerts[:maxAlerts], uint64(len(alerts)) - maxAlerts
	}

	return alerts, 0
}

// Notify implements the Notifier interface.
func (n *Notifier) Notify(ctx context.Context, alerts ...*types.Alert) (bool, error) {
	msg := generateMessage(ctx, n.tmpl, n.logger, n.conf.MaxAlerts, n.jsonnet, n.conf.MessageTemplate, alerts)
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(msg); err != nil {
		return false, err
	}

	resp, err := notify.PostJSON(ctx, n.client, n.conf.URL.String(), &buf)
	if err != nil {
		return true, err
	}
	notify.Drain(resp)

	return n.retrier.Check(resp.StatusCode, nil)
}

func generateMessage(ctx context.Context, tmpl *template.Template, logger log.Logger, maxAlerts uint64, jsonnet *jsonnet.VM, t string, alerts []*types.Alert) *Message {
	alerts, numTruncated := truncateAlerts(maxAlerts, alerts)
	data := notify.GetTemplateData(ctx, tmpl, alerts, logger)

	groupKey, err := notify.ExtractGroupKey(ctx)
	if err != nil {
		level.Error(logger).Log("err", err)
	}

	msg := &Message{
		Version:         "4",
		Data:            data,
		GroupKey:        groupKey.String(),
		TruncatedAlerts: numTruncated,
	}

	if jsonnet != nil {
		message, err := evalJsonnetTemplate(jsonnet, t, data)
		if err != nil {
			return nil
		}
		msg.Message = message
	}
	return msg
}

func evalJsonnetTemplate(vm *jsonnet.VM, t string, data *template.Data) (map[string]string, error) {
	dataImport, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	vm.ExtCode("data", string(dataImport))

	msgJson, err := vm.EvaluateAnonymousSnippetMulti("template.json", t)
	if err != nil {
		return nil, err
	}
	return msgJson, nil
}
