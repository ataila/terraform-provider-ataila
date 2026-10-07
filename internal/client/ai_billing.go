// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/http"
	"time"
)

// Rated AI usage, the AI rate card and tenant rate plans (platform release
// ReleaseAIBilling and later). Decoded by hand: rates keep float64 precision
// (EUR per one million tokens, six decimal places on the platform), and the
// usage reads stay records for the spec-mapped data sources.

// AIRate is one rate row: a tier's price from ValidFrom until ValidTo.
type AIRate struct {
	Tier           string     `json:"tier"`
	EurPer1mInput  float64    `json:"eur_per_1m_input"`
	EurPer1mOutput float64    `json:"eur_per_1m_output"`
	ValidFrom      string     `json:"valid_from"`
	ValidTo        *string    `json:"valid_to"`
	Note           *string    `json:"note"`
	SetBy          *string    `json:"set_by"`
	SetAt          *time.Time `json:"set_at"`
}

// AIRateCardTier is one tier of the rate card.
type AIRateCardTier struct {
	Tier     string  `json:"tier"`
	Current  *AIRate `json:"current"`
	Upcoming *AIRate `json:"upcoming"`
	Seedable *bool   `json:"seedable"`
}

// AIRateCardTierChanged is the answer of PUT /ai/rate-card/{tier}.
type AIRateCardTierChanged struct {
	AIRateCardTier
	AsOf     string       `json:"as_of"`
	Changed  bool         `json:"changed"`
	History  []AIRate     `json:"history"`
	Warnings []ApiWarning `json:"warnings"`
}

// AIRatePlan is the answer of GET, and PUT /tenants/{id}/ai-rate-plan.
type AIRatePlan struct {
	TenantID         string       `json:"tenant_id"`
	TenantName       *string      `json:"tenant_name"`
	AsOf             string       `json:"as_of"`
	ContractIncluded bool         `json:"contract_included"`
	Rates            []AIRate     `json:"rates"`
	Upcoming         []AIRate     `json:"upcoming"`
	Effective        []Record     `json:"effective"`
	Changed          *bool        `json:"changed"`
	Warnings         []ApiWarning `json:"warnings"`
}

// AIRatePlanRate is one tier of a PUT /tenants/{id}/ai-rate-plan body.
type AIRatePlanRate struct {
	Tier           string  `json:"tier"`
	EurPer1mInput  float64 `json:"eur_per_1m_input"`
	EurPer1mOutput float64 `json:"eur_per_1m_output"`
	Note           *string `json:"note,omitempty"`
}

// GetTenantAIUsage reads GET /tenants/{id}/ai-usage (JSON) for a month
// ("" = the current month).
func (a *API) GetTenantAIUsage(ctx context.Context, tenantID, month string) (Record, error) {
	params := &TenantAiUsageGetParams{}
	if month != "" {
		params.Month = &month
	}
	rsp, err := a.raw.TenantAiUsageGetWithResponse(ctx, tenantID, params)
	if err != nil {
		return nil, err
	}
	r, err := decodeJSON[Record]("GET /tenants/"+tenantID+"/ai-usage", rsp.HTTPResponse, rsp.Body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return *r, nil
}

// GetGatewayAIUsage reads GET /ai/gateway/usage for a month ("" = current).
func (a *API) GetGatewayAIUsage(ctx context.Context, month string) (Record, error) {
	params := &AiGatewayUsageGetParams{}
	if month != "" {
		params.Month = &month
	}
	rsp, err := a.raw.AiGatewayUsageGetWithResponse(ctx, params)
	if err != nil {
		return nil, err
	}
	r, err := decodeJSON[Record]("GET /ai/gateway/usage", rsp.HTTPResponse, rsp.Body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return *r, nil
}

// GetAIRateCardRecord reads GET /ai/rate-card as a record (the data source).
func (a *API) GetAIRateCardRecord(ctx context.Context) (Record, error) {
	rsp, err := a.raw.AiRateCardGetWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	r, err := decodeJSON[Record]("GET /ai/rate-card", rsp.HTTPResponse, rsp.Body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return *r, nil
}

// GetAIRateCardTier reads one tier of GET /ai/rate-card; nil when the card
// does not name the tier.
func (a *API) GetAIRateCardTier(ctx context.Context, tier string) (*AIRateCardTier, error) {
	rsp, err := a.raw.AiRateCardGetWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	card, err := decodeJSON[struct {
		Tiers []AIRateCardTier `json:"tiers"`
	}]("GET /ai/rate-card", rsp.HTTPResponse, rsp.Body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	for i := range card.Tiers {
		if card.Tiers[i].Tier == tier {
			return &card.Tiers[i], nil
		}
	}
	return nil, nil
}

// PutAIRateCardTier sends PUT /ai/rate-card/{tier}. validFrom "" = today.
func (a *API) PutAIRateCardTier(ctx context.Context, tier string, eurIn, eurOut float64, validFrom string,
	note *string) (*AIRateCardTierChanged, error) {
	body := map[string]any{"eur_per_1m_input": eurIn, "eur_per_1m_output": eurOut}
	if validFrom != "" {
		body["valid_from"] = validFrom
	}
	if note != nil {
		body["note"] = *note
	}
	r, err := jsonBody(body)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.AiRateCardPutWithBodyWithResponse(ctx, tier, "application/json", r)
	if err != nil {
		return nil, err
	}
	return decodeJSON[AIRateCardTierChanged]("PUT /ai/rate-card/"+tier, rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// GetAIRatePlanRecord reads GET /tenants/{id}/ai-rate-plan as a record.
func (a *API) GetAIRatePlanRecord(ctx context.Context, tenantID string) (Record, error) {
	rsp, err := a.raw.TenantAiRatePlanGetWithResponse(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	r, err := decodeJSON[Record]("GET /tenants/"+tenantID+"/ai-rate-plan", rsp.HTTPResponse, rsp.Body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return *r, nil
}

// GetAIRatePlan reads GET /tenants/{id}/ai-rate-plan.
func (a *API) GetAIRatePlan(ctx context.Context, tenantID string) (*AIRatePlan, error) {
	rsp, err := a.raw.TenantAiRatePlanGetWithResponse(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return decodeJSON[AIRatePlan]("GET /tenants/"+tenantID+"/ai-rate-plan", rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// PutAIRatePlan sends PUT /tenants/{id}/ai-rate-plan: the WHOLE plan (a tier
// left out goes back to the list rate). validFrom "" = today.
func (a *API) PutAIRatePlan(ctx context.Context, tenantID string, rates []AIRatePlanRate,
	validFrom string) (*AIRatePlan, error) {
	if rates == nil {
		rates = []AIRatePlanRate{}
	}
	body := map[string]any{"rates": rates}
	if validFrom != "" {
		body["valid_from"] = validFrom
	}
	r, err := jsonBody(body)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.TenantAiRatePlanPutWithBodyWithResponse(ctx, tenantID, "application/json", r)
	if err != nil {
		return nil, err
	}
	return decodeJSON[AIRatePlan]("PUT /tenants/"+tenantID+"/ai-rate-plan", rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// DeleteAIRatePlan sends DELETE /tenants/{id}/ai-rate-plan: the plan ends
// today and every tier goes back to the list rate.
func (a *API) DeleteAIRatePlan(ctx context.Context, tenantID string) error {
	rsp, err := a.raw.TenantAiRatePlanDeleteWithResponse(ctx, tenantID)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected("DELETE /tenants/"+tenantID+"/ai-rate-plan", rsp.HTTPResponse)
	}
	return nil
}
