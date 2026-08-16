export type ImageCostRoutingFormState = {
  image_cost_routing_enabled?: boolean;
  image_cost_routing_mode?: string;
  image_cost_tolerance_percent?: number | null;
  image_cost_stale_after_seconds?: number | null;
};

export type ImageCostRoutingValidationError =
  | "modeInvalid"
  | "toleranceInvalid"
  | "staleAfterInvalid";

export const validateImageCostRoutingFormState = (
  form: ImageCostRoutingFormState,
): ImageCostRoutingValidationError | null => {
  if (!form.image_cost_routing_enabled) return null;
  if (
    form.image_cost_routing_mode !== "prefer_lowest" &&
    form.image_cost_routing_mode !== "strict_lowest"
  ) {
    return "modeInvalid";
  }
  const tolerance = Number(form.image_cost_tolerance_percent);
  if (!Number.isFinite(tolerance) || tolerance < 0 || tolerance > 100) {
    return "toleranceInvalid";
  }
  const staleAfter = Number(form.image_cost_stale_after_seconds);
  if (
    !Number.isInteger(staleAfter) ||
    staleAfter < 300 ||
    staleAfter > 604800
  ) {
    return "staleAfterInvalid";
  }
  return null;
};
