import { beforeEach, describe, expect, it, vi } from "vitest";

const { get, put } = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  apiClient: { get, put },
}));

import {
  getGatewayRequestObserverSettings,
  updateGatewayRequestObserverSettings,
} from "@/api/admin/settings";

describe("admin gateway request observer settings API", () => {
  beforeEach(() => {
    get.mockReset();
    put.mockReset();
  });

  it("reads and updates the dedicated request observer endpoint", async () => {
    const settings = {
      enabled: true,
      api_key_ids: [11],
      api_key_names: ["maibon-gpt"],
      user_ids: [27],
      user_emails: ["1069167864@qq.com"],
      output_path: "/app/.tmp/observer/requests.jsonl",
    };
    get.mockResolvedValueOnce({ data: settings });
    put.mockResolvedValueOnce({ data: settings });

    await expect(getGatewayRequestObserverSettings()).resolves.toEqual(settings);
    await expect(updateGatewayRequestObserverSettings(settings)).resolves.toEqual(settings);
    expect(get).toHaveBeenCalledWith("/admin/settings/request-observer");
    expect(put).toHaveBeenCalledWith(
      "/admin/settings/request-observer",
      settings,
    );
  });
});
