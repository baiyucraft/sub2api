import { describe, expect, it } from "vitest";

import { validateImageCostRoutingFormState } from "../groupsImageCostRouting";

describe("groups image cost routing validation", () => {
  it("keeps disabled routing backward compatible", () => {
    expect(validateImageCostRoutingFormState({ image_cost_routing_enabled: false })).toBeNull();
  });

  it("accepts both routing modes and boundary values", () => {
    expect(validateImageCostRoutingFormState({
      image_cost_routing_enabled: true,
      image_cost_routing_mode: "prefer_lowest",
      image_cost_tolerance_percent: 0,
      image_cost_stale_after_seconds: 300,
    })).toBeNull();
    expect(validateImageCostRoutingFormState({
      image_cost_routing_enabled: true,
      image_cost_routing_mode: "strict_lowest",
      image_cost_tolerance_percent: 100,
      image_cost_stale_after_seconds: 604800,
    })).toBeNull();
  });

  it("rejects invalid mode, tolerance and stale threshold", () => {
    expect(validateImageCostRoutingFormState({
      image_cost_routing_enabled: true,
      image_cost_routing_mode: "random",
      image_cost_tolerance_percent: 5,
      image_cost_stale_after_seconds: 86400,
    })).toBe("modeInvalid");
    expect(validateImageCostRoutingFormState({
      image_cost_routing_enabled: true,
      image_cost_routing_mode: "prefer_lowest",
      image_cost_tolerance_percent: 101,
      image_cost_stale_after_seconds: 86400,
    })).toBe("toleranceInvalid");
    expect(validateImageCostRoutingFormState({
      image_cost_routing_enabled: true,
      image_cost_routing_mode: "prefer_lowest",
      image_cost_tolerance_percent: 5,
      image_cost_stale_after_seconds: 299,
    })).toBe("staleAfterInvalid");
  });
});
