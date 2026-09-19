import { beforeEach, describe, expect, it, vi } from "vitest";

const { get, put } = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  apiClient: { get, put },
}));

import {
  getGatewayCapacityFailoverSettings,
  updateGatewayCapacityFailoverSettings,
} from "@/api/admin/settings";

describe("admin gateway capacity failover settings API", () => {
  beforeEach(() => {
    get.mockReset();
    put.mockReset();
  });

  it("loads and updates the dedicated runtime settings endpoint", async () => {
    const settings = {
      enabled: true,
      max_switches: 10,
      exhausted_status_code: 503,
    };
    get.mockResolvedValueOnce({ data: settings });
    put.mockResolvedValueOnce({ data: settings });

    await expect(getGatewayCapacityFailoverSettings()).resolves.toEqual(settings);
    await expect(updateGatewayCapacityFailoverSettings(settings)).resolves.toEqual(settings);
    expect(get).toHaveBeenCalledWith("/admin/settings/gateway-capacity-failover");
    expect(put).toHaveBeenCalledWith("/admin/settings/gateway-capacity-failover", settings);
  });
});
