// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"testing"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

func TestReleaseCalls(t *testing.T) {
	m, api := mockAPI(t, 1)
	ctx := context.Background()
	_, tenant := m.AddCustomer("EXAMPLE", "example")
	project := m.AddTestProject(tenant, "shop", "k8s")
	v := "1.0.0"
	acc, err := api.RequestPromotion(ctx, project, ReleasePromotionCreate{Component: "app-api", TargetEnv: "dev", Version: &v})
	if err != nil || acc.Operation.Id != "release:1" || acc.Operation.Status != OperationStatusRunning || acc.Replayed {
		t.Fatalf("promote: %+v %v", acc, err)
	}
	op := acc.Operation
	native, ok := ReleaseOperationID(op.Id)
	if !ok || native != "1" {
		t.Fatalf("native id %q", native)
	}
	for _, bad := range []string{"release:", "release:x", "provision:1", "1"} {
		if _, ok := ReleaseOperationID(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if op, err = api.Operation(ctx, op.Id); err != nil || op.Status != OperationStatusSucceeded {
		t.Fatalf("poll: %+v %v", op, err)
	}
	ro, err := api.GetReleaseOperation(ctx, native)
	if err != nil || ro.Version == nil || *ro.Version != "1.0.0" || ro.SourceEnv != "sandbox" {
		t.Fatalf("read: %+v %v", ro, err)
	}
	// uat takes what dev reported; a version that is not it is refused.
	wrong := "0.9.0"
	_, err = api.RequestPromotion(ctx, project, ReleasePromotionCreate{Component: "app-api", TargetEnv: "uat", Version: &wrong})
	if !IsCode(err, 422, CodeVersionNotAtSource) {
		t.Fatalf("wrong version: %v", err)
	}
	if _, err := api.RequestPromotion(ctx, project, ReleasePromotionCreate{Component: "app-api", TargetEnv: "uat"}); err != nil {
		t.Fatal(err)
	}
	all, err := api.ListReleaseOperations(ctx, project)
	if err != nil || len(all) != 2 || all[0].Id != "2" {
		t.Fatalf("history (page size 1, newest first): %d %v", len(all), err)
	}
	st, err := api.GetReleaseState(ctx, project)
	if err != nil || len(st.Versions) != 1 || len(st.InFlightOperationIds) != 1 {
		t.Fatalf("state %+v %v", st, err)
	}
	// Every promotion carried its own Idempotency-Key.
	keys := map[string]bool{}
	for _, r := range m.Requests() {
		if r.Method == "POST" {
			keys[r.Header.Get(IdempotencyHeader)] = true
		}
	}
	if len(keys) != 3 || keys[""] {
		t.Errorf("idempotency keys %v", keys)
	}

	// The lock: unlocking needs the short name, which is sent only then.
	if l, err := api.PutProdLock(ctx, project, true, "ignored"); err != nil || !l.Locked {
		t.Fatalf("lock %+v %v", l, err)
	}
	reqs := m.Requests()
	if body := string(reqs[len(reqs)-1].Body); body != `{"locked":true}` {
		t.Errorf("lock body %s", body)
	}
	if _, err := api.PutProdLock(ctx, project, false, "wrong"); !IsCode(err, 422, CodeUnlockNotConfirmed) {
		t.Fatalf("unconfirmed unlock: %v", err)
	}
	if l, err := api.PutProdLock(ctx, project, false, "shop"); err != nil || l.Locked {
		t.Fatalf("unlock %+v %v", l, err)
	}
}

// A 202 whose answer was lost on the way back: the retry, under the same
// Idempotency-Key, gets the stored answer with its Location, marked replayed,
// and nothing is booked twice.
func TestAcceptedReplayAfterALostAnswer(t *testing.T) {
	m, api := mockAPI(t, 1)
	ctx := context.Background()
	_, tenant := m.AddCustomer("EXAMPLE", "example")
	project := m.AddTestProject(tenant, "shop", "k8s")
	m.InjectFaults("/projects/"+project+"/release-promotions",
		acctest.Fault{Status: 502, Method: "POST", AfterHandling: true, RetryAfter: "0"})
	v := "1.0.0"
	acc, err := api.RequestPromotion(ctx, project, ReleasePromotionCreate{Component: "app-api", TargetEnv: "dev", Version: &v})
	if err != nil || !acc.Replayed || acc.Operation.Id != "release:1" {
		t.Fatalf("promotion after a lost answer: %+v %v", acc, err)
	}
	reqs := m.Requests()
	if k1, k2 := reqs[len(reqs)-2].Header.Get(IdempotencyHeader), reqs[len(reqs)-1].Header.Get(IdempotencyHeader); k1 == "" || k1 != k2 {
		t.Errorf("the retry's key %q, the first attempt's %q", k2, k1)
	}
	if all, err := api.ListReleaseOperations(ctx, project); err != nil || len(all) != 1 {
		t.Fatalf("booked %d times (%v), want once", len(all), err)
	}
}

func TestAIModelCalls(t *testing.T) {
	m, api := mockAPI(t, 1)
	ctx := context.Background()
	m.AddAINode("ai-a")
	md, err := api.CreateAIModel(ctx, map[string]any{"repo": "example-lab/a-model", "size_gb": 0.1})
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := md.Float("size_gb"); !ok || f != 0.1 {
		t.Errorf("size_gb %v (float64 precision)", md["size_gb"])
	}
	id := md.String("id")
	if _, err := api.CreateAIModel(ctx, map[string]any{"repo": "example-lab/a-model"}); !IsCode(err, 409, CodeRepoTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	m.GiveCentralCopy(id)
	_, acc, err := api.CacheModel(ctx, id, "ai-a")
	if err != nil || acc == nil || acc.Operation.Id != "model-store-run:1" {
		t.Fatalf("cache: %+v %v", acc, err)
	}
	op := acc.Operation
	reqs := m.Requests()
	if k := reqs[len(reqs)-1].Header.Get(IdempotencyHeader); k == "" {
		t.Error("the node-cache PUT carried no Idempotency-Key")
	}
	if _, _, err := api.CacheModel(ctx, id, "ai-a"); !IsCode(err, 409, CodeRunInProgress) {
		t.Fatalf("second run: %v", err)
	}
	for i := 0; i < 3; i++ {
		if op, err = api.Operation(ctx, op.Id); err != nil {
			t.Fatal(err)
		}
	}
	existing, op2, err := api.CacheModel(ctx, id, "ai-a")
	if err != nil || op2 != nil || existing.String("state") != "cached" {
		t.Fatalf("adopt: %v %v %v", existing, op2, err)
	}
	// Uncache without monitoring: a final 503, one attempt.
	before := m.Calls("DELETE", "/ai-models/"+id+"/node-caches/ai-a")
	if _, err := api.UncacheModel(ctx, id, "ai-a"); !IsCode(err, 503, CodeLoadedStateUnknown) {
		t.Fatalf("uncache blind: %v", err)
	}
	if n := m.Calls("DELETE", "/ai-models/"+id+"/node-caches/ai-a") - before; n != 1 {
		t.Errorf("%d attempts, want 1", n)
	}
	nodes, reachable, err := api.ListAINodes(ctx)
	if err != nil || len(nodes) != 1 || reachable {
		t.Fatalf("nodes %v %v %v", nodes, reachable, err)
	}
	m.SetMonitoring(true)
	if _, reachable, _ = api.ListAINodes(ctx); !reachable {
		t.Error("monitoring_reachable not read")
	}
	models, err := api.ListAIModels(ctx, "", "owned", "", "")
	if err != nil || len(models) != 1 {
		t.Fatalf("filtered models %d %v", len(models), err)
	}
}
