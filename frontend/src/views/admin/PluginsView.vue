<template>
  <AppLayout>
    <div class="space-y-6">
      <section
        class="flex flex-col gap-4 border-b border-gray-200 pb-5 dark:border-dark-700 sm:flex-row sm:items-end sm:justify-between"
      >
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
            {{ t("admin.plugins.title") }}
          </h2>
          <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">
            {{ t("admin.plugins.description") }}
          </p>
          <div
            class="mt-3 flex flex-wrap gap-2 text-xs text-gray-600 dark:text-gray-300"
          >
            <span class="rounded bg-gray-100 px-2 py-1 dark:bg-dark-700">{{
              t("admin.plugins.onlyOpenAI")
            }}</span>
            <span class="rounded bg-gray-100 px-2 py-1 dark:bg-dark-700">{{
              t("admin.plugins.noAccountCoupling")
            }}</span>
          </div>
        </div>

        <div class="flex flex-shrink-0 items-center gap-2">
          <input
            ref="fileInput"
            class="hidden"
            type="file"
            accept=".s2plugin,application/zip"
            @change="handleFileSelected"
          />
          <button
            type="button"
            class="btn btn-primary"
            :disabled="uploading"
            @click="selectPackage()"
          >
            <Icon name="upload" size="sm" />
            {{ uploading ? t("common.processing") : t("admin.plugins.upload") }}
          </button>
          <button
            type="button"
            class="btn btn-secondary"
            :disabled="loading"
            :title="t('common.refresh')"
            @click="loadPlugins"
          >
            <Icon name="refresh" size="sm" />
            <span class="sr-only">{{ t("common.refresh") }}</span>
          </button>
        </div>
      </section>

      <p class="text-xs text-gray-500 dark:text-gray-400">
        {{ t("admin.plugins.uploadHint") }}
      </p>

      <div
        class="border border-blue-200 bg-blue-50 px-4 py-3 text-sm text-blue-800 dark:border-blue-900/60 dark:bg-blue-950/30 dark:text-blue-200"
      >
        <p>{{ t("admin.plugins.runtimeNotice") }}</p>
        <p class="mt-1">{{ t("admin.plugins.menuNotice") }}</p>
      </div>

      <div
        v-if="loading"
        class="flex min-h-48 items-center justify-center text-sm text-gray-500"
      >
        {{ t("common.loading") }}
      </div>

      <div
        v-else-if="plugins.length === 0"
        class="flex min-h-56 flex-col items-center justify-center border border-dashed border-gray-300 px-6 text-center dark:border-dark-600"
      >
        <Icon name="cube" size="xl" class="text-gray-400" />
        <p class="mt-3 font-medium text-gray-800 dark:text-gray-200">
          {{ t("admin.plugins.empty") }}
        </p>
        <p class="mt-1 max-w-lg text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.plugins.emptyHint") }}
        </p>
      </div>

      <div v-else class="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <article
          v-for="plugin in plugins"
          :key="plugin.id"
          class="card overflow-hidden border border-gray-200 dark:border-dark-700"
        >
          <div
            class="flex flex-wrap items-start justify-between gap-3 border-b border-gray-100 p-5 dark:border-dark-700"
          >
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <h3
                  class="truncate text-base font-semibold text-gray-900 dark:text-white"
                >
                  {{ plugin.name }}
                </h3>
                <span class="font-mono text-xs text-gray-500"
                  >v{{ plugin.version }}</span
                >
                <span
                  class="rounded px-2 py-0.5 text-xs font-medium"
                  :class="stateClass(plugin.state)"
                >
                  {{ t(`admin.plugins.${plugin.state}`) }}
                </span>
              </div>
              <p class="mt-1 text-xs text-gray-500">
                {{ plugin.plugin_key
                }}<span v-if="plugin.author"> · {{ plugin.author }}</span>
              </p>
              <p
                v-if="plugin.description"
                class="mt-2 text-sm text-gray-600 dark:text-gray-300"
              >
                {{ plugin.description }}
              </p>
            </div>
            <button
              v-if="plugin.manifest.ui?.type !== 'none'"
              type="button"
              class="btn btn-secondary btn-sm"
              @click="openPluginConfiguration(plugin)"
            >
              <Icon name="cog" size="sm" />
              {{ t("admin.plugins.configure") }}
            </button>
          </div>

          <div class="grid grid-cols-1 gap-x-6 gap-y-4 p-5 md:grid-cols-2">
            <div>
              <p class="text-xs font-medium uppercase text-gray-500">
                {{ t("admin.plugins.compatibility") }}
              </p>
              <div class="mt-2 flex items-center gap-2">
                <span
                  class="rounded px-2 py-0.5 text-xs font-medium"
                  :class="compatibilityClass(plugin.compatibility.status)"
                >
                  {{ t(`admin.plugins.${plugin.compatibility.status}`) }}
                </span>
                <span class="text-xs text-gray-500 dark:text-gray-400">{{
                  plugin.compatibility.message
                }}</span>
              </div>
              <dl
                class="mt-3 grid grid-cols-[auto,1fr] gap-x-3 gap-y-1 text-xs"
              >
                <dt class="text-gray-500">
                  {{ t("admin.plugins.currentVersion") }}
                </dt>
                <dd class="font-mono text-gray-800 dark:text-gray-200">
                  {{ plugin.compatibility.current_sub2api_version }}
                </dd>
                <dt class="text-gray-500">
                  {{ t("admin.plugins.requiredVersion") }}
                </dt>
                <dd class="font-mono text-gray-800 dark:text-gray-200">
                  {{ plugin.compatibility.required_sub2api_version }}
                </dd>
                <dt class="text-gray-500">
                  {{ t("admin.plugins.recommendedVersion") }}
                </dt>
                <dd class="font-mono text-gray-800 dark:text-gray-200">
                  {{ plugin.compatibility.recommended_sub2api_version || "-" }}
                </dd>
                <dt class="text-gray-500">{{ t('admin.plugins.minimumSDK') }}</dt>
                <dd class="break-words font-mono text-gray-800 dark:text-gray-200" data-testid="plugin-minimum-sdk">
                  {{ t('admin.plugins.sdkRequirements', { plugin_protocol: plugin.manifest.requires.plugin_protocol, transport_api: plugin.manifest.requires.transport_api, ui_bridge: plugin.manifest.requires.ui_bridge }) }}
                  <span v-if="plugin.manifest.requires.host_service_api" class="block">Host Service API {{ plugin.manifest.requires.host_service_api }}</span>
                  <ul v-if="plugin.manifest.requires.host_features?.length" class="mt-1 space-y-1">
                    <li v-for="feature in plugin.manifest.requires.host_features" :key="feature">{{ feature }}</li>
                  </ul>
                </dd>
              </dl>
            </div>

            <div>
              <p class="text-xs font-medium uppercase text-gray-500">
                {{ t("admin.plugins.runtime") }}
              </p>
              <div class="mt-2 flex flex-wrap gap-2 text-xs">
                <span
                  class="rounded px-2 py-0.5"
                  :class="
                    plugin.runtime_healthy
                      ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
                      : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
                  "
                >
                  {{
                    plugin.runtime_healthy
                      ? t("admin.plugins.healthy")
                      : t("admin.plugins.unhealthy")
                  }}
                </span>
                <span
                  class="rounded bg-gray-100 px-2 py-0.5 text-gray-600 dark:bg-dark-700 dark:text-gray-300"
                >
                  {{ t("admin.plugins.signature") }}:
                  {{ t(`admin.plugins.${plugin.signature_status}`) }}
                </span>
              </div>
              <p
                v-if="plugin.last_error"
                class="mt-3 break-words text-xs text-red-600 dark:text-red-400"
              >
                {{ plugin.last_error }}
              </p>
              <p
                v-else-if="plugin.runtime_message"
                class="mt-3 break-words text-xs text-gray-500"
              >
                {{ plugin.runtime_message }}
              </p>
            </div>

            <div class="md:col-span-2">
              <label
                class="flex items-center justify-between gap-4 text-xs font-medium text-gray-600 dark:text-gray-300"
              >
                <span>{{ t("admin.plugins.rollout") }}</span>
                <span class="w-11 text-right font-mono"
                  >{{
                    rolloutValues[plugin.id] ?? currentRollout(plugin)
                  }}%</span
                >
              </label>
              <input
                :value="rolloutValues[plugin.id] ?? currentRollout(plugin)"
                type="range"
                min="1"
                max="100"
                step="1"
                class="mt-2 w-full accent-primary-600"
                :disabled="hasEnabledBinding(plugin)"
                @input="setRollout(plugin.id, $event)"
              />
            </div>
          </div>

          <div
            class="flex flex-wrap justify-end gap-2 border-t border-gray-100 px-5 py-4 dark:border-dark-700"
          >
            <button type="button" class="btn btn-secondary btn-sm" :disabled="uploading || busyID === plugin.id" @click="selectPackage(plugin)">
              <Icon name="upload" size="sm" />
              {{ t('admin.plugins.upgrade') }}
            </button>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="busyID === plugin.id"
              @click="testPlugin(plugin)"
            >
              <Icon name="beaker" size="sm" />
              {{ t("admin.plugins.test") }}
            </button>
            <button
              v-if="hasEnabledBinding(plugin)"
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="busyID === plugin.id"
              @click="disablePlugin(plugin)"
            >
              <Icon name="ban" size="sm" />
              {{ t("admin.plugins.disable") }}
            </button>
            <button
              v-else
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="
                busyID === plugin.id ||
                plugin.state === 'starting' ||
                !plugin.compatibility.compatible
              "
              @click="enablePlugin(plugin)"
            >
              <Icon name="play" size="sm" />
              {{ t("admin.plugins.enable") }}
            </button>
            <button
              type="button"
              class="btn btn-danger btn-sm"
              :disabled="busyID === plugin.id || hasEnabledBinding(plugin)"
              @click="uninstallPlugin(plugin)"
            >
              <Icon name="trash" size="sm" />
              {{ t("admin.plugins.uninstall") }}
            </button>
          </div>
        </article>
      </div>

      <BaseDialog
        :show="configPlugin !== null"
        :title="
          t('admin.plugins.configTitle', { name: configPlugin?.name || '' })
        "
        width="full"
        :close-on-escape="!secretsEditor"
        @close="closeConfiguration"
      >
        <div
          class="relative min-h-[520px] overflow-hidden bg-gray-50 dark:bg-dark-900"
          :style="{ height: `${iframeHeight}px` }"
        >
          <div
            v-if="uiLoading"
            class="absolute inset-0 z-10 flex items-center justify-center text-sm text-gray-500"
          >
            {{ t("admin.plugins.loadingUI") }}
          </div>
          <div
            v-if="uiError"
            class="absolute inset-0 z-20 flex flex-col items-center justify-center p-8 text-center"
          >
            <Icon name="exclamationTriangle" size="xl" class="text-amber-500" />
            <p class="mt-3 font-medium text-gray-800 dark:text-gray-200">
              {{ t("admin.plugins.uiUnavailable") }}
            </p>
            <p class="mt-1 max-w-xl text-sm text-gray-500">{{ uiError }}</p>
          </div>
          <iframe
            v-if="uiSession"
            ref="pluginFrame"
            :src="uiSession.url"
            sandbox="allow-scripts"
            referrerpolicy="no-referrer"
            class="h-full w-full border-0 bg-white dark:bg-dark-900"
            :title="
              t('admin.plugins.configTitle', { name: configPlugin?.name || '' })
            "
            @load="handlePluginFrameLoad"
          />
        </div>
      </BaseDialog>

      <PluginSecretsDialog v-if="secretsEditor" :key="secretsEditor.generation" :name="configPlugin?.name || ''" :fields="secretsEditor.fields"
        :configured="secretsConfigured" :loading="secretsLoading" :ready="secretsReady"
        :saving="secretsSaving" :error="secretsError" @cancel="cancelSecretsEditor" @save="savePluginSecrets" />
      <TotpStepUpDialog :controller="pluginStepUp" />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, shallowRef } from "vue";
import { useRouter } from 'vue-router';
import { useI18n } from "vue-i18n";
import {
  adminAPI,
  type PluginInstallation,
  type PluginUISession,
} from "@/api/admin";
import { useAppStore } from "@/stores";
import AppLayout from "@/components/layout/AppLayout.vue";
import BaseDialog from "@/components/common/BaseDialog.vue";
import Icon from "@/components/icons/Icon.vue";
import TotpStepUpDialog from "@/components/auth/TotpStepUpDialog.vue";
import PluginSecretsDialog from '@/components/admin/plugins/PluginSecretsDialog.vue';
import { isSecretsEditRequest, parsePluginAction, pluginRunning, pluginSecretFields, preparePluginConfig,
  projectPluginConfig, projectPluginResources, projectSecretFlags } from './pluginBridge';
import {
  isStepUpBlocked,
  isStepUpCancelled,
  stepUpBlockReason,
  useStepUp,
} from "@/composables/useStepUp";

interface PluginBridgeMessage {
  source?: string;
  bridge_token?: string;
  type?: string;
  request_id?: string;
  config?: unknown;
  height?: unknown;
  level?: unknown;
  message?: unknown;
  action_id?: unknown;
  name?: unknown;
  payload?: unknown;
}

const { t, locale } = useI18n();
const router = useRouter();
const appStore = useAppStore();
const pluginStepUp = useStepUp();
const plugins = ref<PluginInstallation[]>([]);
const loading = ref(false);
const uploading = ref(false);
const busyID = ref<number | null>(null);
const fileInput = ref<HTMLInputElement | null>(null);
const upgradeTarget = ref<PluginInstallation | null>(null);
const rolloutValues = ref<Record<number, number>>({});
const configPlugin = ref<PluginInstallation | null>(null);
const uiSession = ref<PluginUISession | null>(null);
const pluginFrame = ref<HTMLIFrameElement | null>(null);
const uiLoading = ref(false);
const uiError = ref("");
const iframeHeight = ref(640);
const pluginFrameLoaded = ref(false);
let configurationGeneration = 0;
const pendingBridgeRequests = new Map<string, { timeout: number; request: PluginBridgeMessage }>();
interface SecretsEditor {
  generation: number;
  request: PluginBridgeMessage;
  session: PluginUISession;
  pluginID: number;
  fields: string[];
}
const secretsEditor = shallowRef<SecretsEditor | null>(null);
const secretsConfigured = ref<Record<string, boolean>>({});
const secretsLoading = ref(false);
const secretsReady = ref(false);
const secretsSaving = ref(false);
const secretsError = ref('');
let configWritePending = false;
let secretsEditorGeneration = 0;

function resetSecretsEditor(): void {
  secretsEditor.value = null;
  secretsConfigured.value = {};
  secretsLoading.value = false;
  secretsReady.value = false;
  secretsSaving.value = false;
  secretsError.value = '';
}

function currentSecretsEditor(editor: SecretsEditor): boolean {
  return secretsEditor.value === editor && uiSession.value === editor.session && configPlugin.value?.id === editor.pluginID &&
    pendingBridgeRequests.get(editor.request.request_id?.trim() || '')?.request === editor.request;
}

function cancelSecretsEditor(): void {
  const editor = secretsEditor.value;
  if (!editor || secretsSaving.value) return;
  postBridgeResult(editor.request, { ok: false, code: 'cancelled' });
  resetSecretsEditor();
}

async function openSecretsEditor(request: PluginBridgeMessage, session: PluginUISession, plugin: PluginInstallation): Promise<void> {
  const fields = pluginSecretFields(plugin.manifest);
  if (!fields.length || !isSecretsEditRequest(request) || secretsEditor.value || configWritePending) {
    throw new Error(t('admin.plugins.bridgeRejected'));
  }
  const editor: SecretsEditor = { generation: ++secretsEditorGeneration, request, session, pluginID: plugin.id, fields };
  secretsEditor.value = editor;
  secretsConfigured.value = projectSecretFlags({}, fields);
  secretsLoading.value = true;
  try {
    const config = await adminAPI.plugins.getConfig(plugin.id);
    if (!currentSecretsEditor(editor)) return;
    secretsConfigured.value = projectSecretFlags(config._host_secrets, fields);
    secretsReady.value = true;
  } catch {
    if (currentSecretsEditor(editor)) secretsError.value = t('admin.plugins.secretsLoadFailed');
  } finally {
    if (currentSecretsEditor(editor)) secretsLoading.value = false;
  }
}

async function savePluginSecrets(values: Record<string, string>): Promise<void> {
  const editor = secretsEditor.value;
  if (!editor || !currentSecretsEditor(editor) || !secretsReady.value || secretsSaving.value) return;
  if (Object.entries(values).some(([field, value]) => !editor.fields.includes(field) || typeof value !== 'string')) return;
  if (!Object.keys(values).length) {
    postBridgeResult(editor.request, { ok: true, result: { configured: { ...secretsConfigured.value } } });
    resetSecretsEditor();
    return;
  }
  secretsSaving.value = true;
  secretsError.value = '';
  try {
    const config = await pluginStepUp.run(() => {
      if (!currentSecretsEditor(editor)) throw new Error(t('admin.plugins.bridgeRejected'));
      return adminAPI.plugins.saveSecrets(editor.pluginID, values);
    });
    if (!currentSecretsEditor(editor)) return;
    postBridgeResult(editor.request, { ok: true, result: { configured: projectSecretFlags(config._host_secrets, editor.fields) } });
    resetSecretsEditor();
  } catch (error: unknown) {
    if (currentSecretsEditor(editor) && !isStepUpCancelled(error)) {
      secretsError.value = t('admin.plugins.secretsSaveFailed');
    }
  } finally {
    // These values came only from the trusted host dialog, never from the iframe.
    for (const field of Object.keys(values)) delete values[field];
    if (currentSecretsEditor(editor)) secretsSaving.value = false;
  }
}

function errorMessage(error: unknown): string {
  if (typeof error === "object" && error !== null && "message" in error) {
    return String(
      (error as { message?: unknown }).message || t("common.unknownError"),
    );
  }
  return t("common.unknownError");
}

function reportSensitiveActionError(error: unknown): void {
  if (isStepUpCancelled(error)) return;
  if (isStepUpBlocked(error)) {
    appStore.showError(
      stepUpBlockReason(error) === "STEP_UP_ADMIN_API_KEY_FORBIDDEN"
        ? t("stepUp.adminApiKeyForbidden")
        : t("stepUp.notEnabled"),
    );
    return;
  }
  appStore.showError(errorMessage(error));
}

async function loadPlugins(): Promise<void> {
  loading.value = true;
  try {
    plugins.value = await adminAPI.plugins.list();
    for (const plugin of plugins.value) {
      rolloutValues.value[plugin.id] = currentRollout(plugin);
    }
  } catch (error: unknown) {
    appStore.showError(errorMessage(error));
  } finally {
    loading.value = false;
  }
}

async function handleFileSelected(event: Event): Promise<void> {
  const target = event.target as HTMLInputElement;
  const file = target.files?.[0];
  const upgradePlugin = upgradeTarget.value;
  upgradeTarget.value = null;
  target.value = "";
  if (!file || !file.name.toLowerCase().endsWith(".s2plugin")) {
    appStore.showError(t("admin.plugins.fileRequired"));
    return;
  }
  uploading.value = true;
  try {
    await pluginStepUp.run(() => upgradePlugin
      ? adminAPI.plugins.upgrade(upgradePlugin.id, file)
      : adminAPI.plugins.upload(file));
    if (upgradePlugin?.id === configPlugin.value?.id) closeConfiguration();
    appStore.showSuccess(t(upgradePlugin ? 'admin.plugins.upgradeSuccess' : 'admin.plugins.uploadSuccess'));
    await loadPlugins();
  } catch (error: unknown) {
    reportSensitiveActionError(error);
  } finally {
    uploading.value = false;
  }
}

function selectPackage(plugin: PluginInstallation | null = null): void {
  upgradeTarget.value = plugin;
  fileInput.value?.click();
}

function currentRollout(plugin: PluginInstallation): number {
  return (
    plugin.bindings.find(
      (binding) => binding.capability === "openai.oauth.outbound_transport.v1",
    )?.rollout_percent || 100
  );
}

function hasEnabledBinding(plugin: PluginInstallation): boolean {
  return plugin.bindings.some((binding) => binding.enabled);
}

function openPluginConfiguration(plugin: PluginInstallation): void {
  if (plugin.manifest.ui?.type === 'native') {
    void router.push(`/admin/plugins/${encodeURIComponent(plugin.plugin_key)}`)
    return
  }
  if (plugin.manifest.ui?.type === 'none') return
  openConfiguration(plugin)
}

function setRollout(id: number, event: Event): void {
  const value = Number((event.target as HTMLInputElement).value);
  rolloutValues.value[id] = Math.min(100, Math.max(1, value));
}

async function enablePlugin(plugin: PluginInstallation): Promise<void> {
  let acceptUntested = false;
  if (!plugin.compatibility.tested) {
    acceptUntested = window.confirm(t("admin.plugins.confirmUntested"));
    if (!acceptUntested) return;
  }
  busyID.value = plugin.id;
  try {
    await pluginStepUp.run(() =>
      adminAPI.plugins.enable(
        plugin.id,
        rolloutValues.value[plugin.id] || 100,
        acceptUntested,
      ),
    );
    appStore.showSuccess(t("admin.plugins.enableSuccess"));
    await loadPlugins();
  } catch (error: unknown) {
    reportSensitiveActionError(error);
  } finally {
    busyID.value = null;
  }
}

async function disablePlugin(plugin: PluginInstallation): Promise<void> {
  if (!window.confirm(t("admin.plugins.confirmDisable"))) return;
  busyID.value = plugin.id;
  try {
    await pluginStepUp.run(() => adminAPI.plugins.disable(plugin.id));
    appStore.showSuccess(t("admin.plugins.disableSuccess"));
    await loadPlugins();
  } catch (error: unknown) {
    reportSensitiveActionError(error);
  } finally {
    busyID.value = null;
  }
}

async function uninstallPlugin(plugin: PluginInstallation): Promise<void> {
  if (!window.confirm(t("admin.plugins.confirmUninstall"))) return;
  busyID.value = plugin.id;
  try {
    await pluginStepUp.run(() => adminAPI.plugins.remove(plugin.id));
    appStore.showSuccess(t("admin.plugins.uninstallSuccess"));
    await loadPlugins();
  } catch (error: unknown) {
    reportSensitiveActionError(error);
  } finally {
    busyID.value = null;
  }
}

async function testPlugin(plugin: PluginInstallation): Promise<void> {
  busyID.value = plugin.id;
  try {
    const result = await pluginStepUp.run(() =>
      adminAPI.plugins.test(plugin.id),
    );
    if (result.success)
      appStore.showSuccess(result.message || t("admin.plugins.testSuccess"));
    else appStore.showError(result.message || t("common.error"));
  } catch (error: unknown) {
    reportSensitiveActionError(error);
  } finally {
    busyID.value = null;
  }
}

async function openConfiguration(plugin: PluginInstallation): Promise<void> {
  const generation = ++configurationGeneration;
  configPlugin.value = plugin;
  uiSession.value = null;
  pluginFrameLoaded.value = false;
  clearPendingBridgeRequests();
  uiLoading.value = true;
  uiError.value = "";
  iframeHeight.value = 640;
  try {
    const session = await adminAPI.plugins.createUISession(plugin.id);
    if (generation === configurationGeneration) uiSession.value = session;
  } catch (error: unknown) {
    if (generation !== configurationGeneration) return;
    uiLoading.value = false;
    uiError.value = errorMessage(error);
  }
}

function closeConfiguration(): void {
  configurationGeneration++;
  clearPendingBridgeRequests();
  pluginFrameLoaded.value = false;
  configPlugin.value = null;
  uiSession.value = null;
  uiLoading.value = false;
  uiError.value = "";
}

function clearPendingBridgeRequests(): void {
  resetSecretsEditor();
  for (const { timeout } of pendingBridgeRequests.values()) window.clearTimeout(timeout);
  pendingBridgeRequests.clear();
}

function handlePluginFrameLoad(): void {
  // A load can also be caused by a plugin navigating its iframe. Drop all
  // outstanding responses so a late config response is never sent to the new document.
  if (pluginFrameLoaded.value) clearPendingBridgeRequests();
  pluginFrameLoaded.value = true;
  uiLoading.value = false;
}

function registerBridgeRequest(requestID: string, request: PluginBridgeMessage): void {
  const timeout = window.setTimeout(() => {
    if (secretsEditor.value?.request === request) resetSecretsEditor();
    pendingBridgeRequests.delete(requestID);
  }, request.type === 'plugin.secrets.edit' ? 10 * 60_000 : 30_000);
  pendingBridgeRequests.set(requestID, { timeout, request });
}

function postBridgeResult(
  request: PluginBridgeMessage,
  payload: Record<string, unknown>,
): void {
  if (!pluginFrame.value?.contentWindow || !uiSession.value) return;
  const requestID = typeof request.request_id === "string" ? request.request_id.trim() : "";
  const pending = pendingBridgeRequests.get(requestID);
  if (!requestID || !pending || pending.request !== request) return;
  window.clearTimeout(pending.timeout);
  pendingBridgeRequests.delete(requestID);
  pluginFrame.value.contentWindow.postMessage(
    {
      source: "sub2api-plugin-host",
      bridge_token: uiSession.value.bridge_token,
      type: `${request.type}.result`,
      request_id: requestID,
      ...payload,
    },
    // The sandboxed iframe has an opaque origin, so no fixed target origin exists.
    // Pending request tracking plus load invalidation prevents cross-navigation leaks.
    "*",
  );
}

async function handleBridgeMessage(event: MessageEvent): Promise<void> {
  if (
    !uiSession.value ||
    !configPlugin.value ||
    event.source !== pluginFrame.value?.contentWindow ||
    event.origin !== "null"
  )
    return;
  const message = event.data as PluginBridgeMessage;
  if (
    !message ||
    message.source !== "sub2api-plugin-ui" ||
    message.bridge_token !== uiSession.value.bridge_token
  )
    return;

  const pluginID = configPlugin.value.id;
  const secretFields = pluginSecretFields(configPlugin.value.manifest);
  const session = uiSession.value;
  const requestID = typeof message.request_id === "string" ? message.request_id.trim() : "";
  const expectsResponse =
    message.type === "config.load" ||
    message.type === "config.save" ||
    message.type === "config.test" ||
    message.type === "plugin.status" ||
    message.type === "plugin.resources" ||
    message.type === 'plugin.secrets.edit' ||
    message.type === "plugin.action";
  if (expectsResponse) {
    if (!requestID || requestID.length > 128 || pendingBridgeRequests.has(requestID) || pendingBridgeRequests.size >= 32) return;
    registerBridgeRequest(requestID, message);
  }

  try {
    switch (message.type) {
      case "sub2api.plugin.ready":
        uiLoading.value = false;
        break;
      case "config.load": {
        const config = await adminAPI.plugins.getConfig(pluginID);
        postBridgeResult(message, { ok: true, config: projectPluginConfig(config, secretFields), host: { locale: locale?.value || 'en' } });
        break;
      }
      case "config.save": {
        if (
          !message.config ||
          typeof message.config !== "object" ||
          Array.isArray(message.config) || secretsEditor.value || configWritePending
        ) {
          throw new Error(t("admin.plugins.bridgeRejected"));
        }
        const fields = secretFields;
        configWritePending = true;
        try {
          const config = await pluginStepUp.run(() => {
            if (uiSession.value !== session || pendingBridgeRequests.get(requestID)?.request !== message) throw new Error(t('admin.plugins.bridgeRejected'));
            return adminAPI.plugins.saveConfig(pluginID, preparePluginConfig(message.config, fields));
          });
          postBridgeResult(message, { ok: true, config: projectPluginConfig(config, fields) });
          if (uiSession.value === session) appStore.showSuccess(t('common.saved'));
        } finally { configWritePending = false; }
        break;
      }
      case 'plugin.secrets.edit': {
        await openSecretsEditor(message, session, configPlugin.value);
        break;
      }
      case "config.test": {
        const result = await pluginStepUp.run(() => {
          if (uiSession.value !== session) throw new Error(t('admin.plugins.bridgeRejected'));
          return adminAPI.plugins.test(pluginID);
        });
        postBridgeResult(message, { ok: result.success, result });
        // A successful result is delivered back to the plugin UI, which owns how it
        // presents it (inline status, or an explicit ui.notify). Only force a host
        // toast on failure so genuine errors are never silently dropped — plugins
        // may call config.test for lightweight status polling, not just as an
        // explicit "test" action, and those must not spam a success toast.
        if (!result.success)
          appStore.showError(result.message || t("common.error"));
        break;
      }
      case "plugin.status": {
        // Read-only runtime status (the plugin's Health snapshot). It has no side
        // effects, so it is intentionally NOT step-up gated and never raises a host
        // toast — the plugin UI renders it however it likes. This is the generic
        // channel for any plugin to surface live state without abusing config.test.
        const result = await adminAPI.plugins.status(pluginID);
        postBridgeResult(message, { ok: true, result });
        break;
      }
      case 'plugin.resources': {
        const resources = projectPluginResources(await adminAPI.plugins.resources(pluginID));
        postBridgeResult(message, { ok: true, resources });
        break;
      }
      case 'plugin.action': {
        const action = parsePluginAction(message);
        if (!action) throw new Error(t('admin.plugins.bridgeRejected'));
        if (!pluginRunning(configPlugin.value)) throw new Error(t('admin.plugins.actionUnavailable'));
        const result = await pluginStepUp.run(async () => {
          if (uiSession.value !== session) throw new Error(t('admin.plugins.bridgeRejected'));
          const current = (await adminAPI.plugins.list()).find(p => p.id === pluginID);
          if (!current || !pluginRunning(current)) throw new Error(t('admin.plugins.actionUnavailable'));
          if (uiSession.value !== session) throw new Error(t('admin.plugins.bridgeRejected'));
          return adminAPI.plugins.action(pluginID, action);
        });
        postBridgeResult(message, { ok: true, result });
        break;
      }
      case "ui.resize": {
        const height = Number(message.height);
        if (Number.isFinite(height))
          iframeHeight.value = Math.min(960, Math.max(520, Math.round(height)));
        break;
      }
      case "ui.notify": {
        const text =
          typeof message.message === "string"
            ? message.message.slice(0, 500)
            : "";
        if (!text) break;
        if (message.level === "error") appStore.showError(text);
        else if (message.level === "success") appStore.showSuccess(text);
        else appStore.showInfo(text);
        break;
      }
    }
  } catch (error: unknown) {
    if (isStepUpBlocked(error)) reportSensitiveActionError(error);
    postBridgeResult(message, {
      ok: false,
      error: isStepUpCancelled(error) ? t("common.cancel") : t('admin.plugins.bridgeRequestFailed'),
    });
  }
}

function stateClass(state: PluginInstallation["state"]): string {
  if (state === "enabled")
    return "bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300";
  if (state === "error" || state === "incompatible")
    return "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300";
  if (state === "starting")
    return "bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300";
  return "bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300";
}

function compatibilityClass(
  status: PluginInstallation["compatibility"]["status"],
): string {
  if (status === "compatible")
    return "bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300";
  if (status === "untested")
    return "bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300";
  return "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300";
}

onMounted(() => {
  window.addEventListener("message", handleBridgeMessage);
  void loadPlugins();
});

onBeforeUnmount(() => {
  window.removeEventListener("message", handleBridgeMessage);
  clearPendingBridgeRequests();
});
</script>
