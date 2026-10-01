// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// acceptedOperation is the operation of a 202, whose Id is the one the
// Location header names. A replayed answer (Idempotent-Replayed: one of this
// request's own retries had already reached the platform) is logged: the work
// was started once, by that earlier attempt.
func acceptedOperation(ctx context.Context, acc *client.Accepted, doing string) *client.Operation {
	if acc.Replayed {
		tflog.Info(ctx, "the platform replayed its stored answer to an earlier attempt of this request; nothing was started twice",
			map[string]any{"operation_id": acc.Operation.Id, "while": doing})
	}
	return acc.Operation
}

// pollOperation polls GET /operations/{id} until the operation ends
// (succeeded or failed), until stop says the caller has seen enough, or until
// ctx is done. It returns the last answer (first when it already qualifies)
// and ctx.Err() when the wait ran out.
func pollOperation(ctx context.Context, api *client.API, id string, first *client.Operation,
	stop func(*client.Operation) bool) (*client.Operation, error) {
	last := first
	for {
		if last != nil {
			if last.Status == client.OperationStatusSucceeded || last.Status == client.OperationStatusFailed {
				return last, nil
			}
			if stop != nil && stop(last) {
				return last, nil
			}
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(operationPollInterval):
		}
		op, err := api.Operation(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return last, ctx.Err()
			}
			return last, err
		}
		last = op
	}
}

// operationDispatch is an operation's dispatch mode ("" when not reported).
func operationDispatch(op *client.Operation) string {
	if op == nil || op.DispatchMode == nil {
		return ""
	}
	return string(*op.DispatchMode)
}

// fakesDispatch reports a dispatch mode under which the operation never
// completes: nothing is executed.
func fakesDispatch(mode string) bool {
	return mode == "dryrun" || mode == "simulate"
}

// operationErrorText renders an operation's error and message.
func operationErrorText(op *client.Operation) string {
	var lines []string
	if op.Error != nil {
		keys := make([]string, 0, len(*op.Error))
		for k := range *op.Error {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("%s: %v", k, (*op.Error)[k]))
		}
	}
	if m := ptrString(op.Message); m != "" {
		lines = append(lines, m)
	}
	return strings.Join(lines, "\n")
}

// operationErrorCode is the code in an operation's error ("" without one).
func operationErrorCode(op *client.Operation) string {
	if op == nil || op.Error == nil {
		return ""
	}
	code, _ := (*op.Error)["code"].(string)
	return code
}
