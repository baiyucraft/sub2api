import { beforeEach, describe, expect, it, vi } from "vitest";

const { get, put } = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  apiClient: { get, put },
}));

import {
  getOpenAITTFTGuardSettings,
  updateOpenAITTFTGuardSettings,
} from "@/api/admin/settings";

describe("admin OpenAI TTFT Guard settings API", () => {
  beforeEach(() => {
    get.mockReset();
    put.mockReset();
  });

  it("reads and updates the dedicated settings endpoint", async () => {
    const settings = {
      enabled: true,
      degradation_ttft_seconds: 20,
      min_samples: 5,
    };
    get.mockResolvedValueOnce({ data: settings });
    put.mockResolvedValueOnce({ data: settings });

    await expect(getOpenAITTFTGuardSettings()).resolves.toEqual(settings);
    await expect(updateOpenAITTFTGuardSettings(settings)).resolves.toEqual(settings);
    expect(get).toHaveBeenCalledWith("/admin/settings/openai-ttft-guard");
    expect(put).toHaveBeenCalledWith(
      "/admin/settings/openai-ttft-guard",
      settings,
    );
  });
});
