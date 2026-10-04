import { defineComponent, h, type PropType } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminGroup, CodexModelsManifestConfig } from "@/types";
import GroupsView from "@/views/admin/GroupsView.vue";
import { adminAPI } from "@/api/admin";

const {
  listGroups,
  getModelAllowlistCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
}));

vi.mock("@/api/admin", () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getAll: vi.fn(),
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      create: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
      duplicate: vi.fn(),
      updateSortOrder: vi.fn(),
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn(),
    },
  },
}));

vi.mock("@/stores/app", () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}));

vi.mock("@/stores/onboarding", () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn(),
  }),
}));

vi.mock("vue-i18n", async () => {
  const actual = await vi.importActual<typeof import("vue-i18n")>("vue-i18n");
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  };
});

const sourceGroup = {
  id: 42,
  name: "OpenAI",
  description: null,
  platform: "openai",
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: "active",
  subscription_type: "standard",
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  long_context_pricing_enabled: true,
  force_openai_fast: false,
  free_openai_fast: false,
  model_pricing: [],
  profit_control_enabled: false,
  profit_min_margin: 0,
  profit_safety_buffer: 0,
  allow_image_generation: false,
  allow_batch_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null,
  video_price_720p: null,
  video_price_1080p: null,
  web_search_price_per_call: null,
  search_price_per_1k: null,
  audio_realtime_price_per_min: null,
  audio_tts_price_per_million_chars: null,
  audio_stt_price_per_hour: null,
  peak_rate_enabled: false,
  peak_start: "",
  peak_end: "",
  peak_rate_multiplier: 1,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  allow_live: false,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 1,
  active_account_count: 1,
  rate_limited_account_count: 0,
  models_list_config: undefined,
  codex_models_manifest_config: {
    enabled: false,
    account_ids: [],
    fallback_to_scheduler: false,
  },
  sort_order: 10,
} satisfies AdminGroup;

const AppLayoutStub = defineComponent({
  template: "<main><slot /></main>",
});

const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
});

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
  },
  template: '<div><div v-if="data.length"><slot name="cell-actions" :row="data[0]" /></div></div>',
});

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
});

const CodexManifestAccountsFieldStub = defineComponent({
  name: "CodexManifestAccountsField",
  props: {
    platform: String,
    modelValue: {
      type: Object as PropType<CodexModelsManifestConfig>,
      required: true,
    },
  },
  emits: ["update:modelValue"],
  setup(props, { emit, expose }) {
    expose({ validate: () => true, resetValidation: () => undefined });
    return () =>
      h("div", { "data-testid": "codex-manifest-field" }, [
        h(
          "output",
          { "data-testid": "codex-manifest-value" },
          JSON.stringify(props.modelValue),
        ),
        h(
          "button",
          {
            type: "button",
            "data-testid": "codex-manifest-enable",
            onClick: () =>
              emit("update:modelValue", {
                ...props.modelValue,
                enabled: true,
              }),
          },
          "enable",
        ),
        h(
          "button",
          {
            type: "button",
            "data-testid": "codex-manifest-select-account",
            onClick: () =>
              emit("update:modelValue", {
                ...props.modelValue,
                account_ids: [17],
              }),
          },
          "select account",
        ),
      ]);
  },
});

const mountView = () =>
  mount(GroupsView, {
    global: {
      plugins: [createPinia()],
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        ReasoningEffortPolicyFields: true,
        CodexManifestAccountsField: CodexManifestAccountsFieldStub,
        PricingEntryCard: true,
        VueDraggable: true,
      },
    },
  });

describe("GroupsView Codex manifest binding", () => {
  beforeEach(() => {
    localStorage.clear();
    listGroups.mockReset();
    getModelAllowlistCandidates.mockReset();
    getUsageSummary.mockReset();
    getCapacitySummary.mockReset();
    getLiveCapability.mockReset();

    listGroups.mockResolvedValue({
      items: [sourceGroup],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    });
    getModelAllowlistCandidates.mockResolvedValue([]);
    getUsageSummary.mockResolvedValue([]);
    getCapacitySummary.mockResolvedValue([]);
    getLiveCapability.mockResolvedValue({ supported: false });
  });

  it("saves and reloads fixed Gateway accounts without clearing the configuration", async () => {
    const group = { ...sourceGroup, platform: 'openai_codex' as const };
    listGroups.mockResolvedValue({ items: [group], total: 1, page: 1, page_size: 20, pages: 1 });
    vi.mocked(adminAPI.groups.update).mockResolvedValue(group as never);
    vi.mocked(adminAPI.accounts.getById).mockResolvedValue({ id: 17, name: 'Gateway account' } as never);
    const wrapper = mountView();
    await flushPromises();
    const edit = () => wrapper.findAll('button').find(button => button.text().includes('common.edit'))!;
    await edit().trigger('click');
    await flushPromises();
    expect(wrapper.getComponent(CodexManifestAccountsFieldStub).props('platform')).toBe('openai_codex');
    await wrapper.get('[data-testid="codex-manifest-enable"]').trigger('click');
    await wrapper.get('[data-testid="codex-manifest-select-account"]').trigger('click');
    await wrapper.get('form#edit-group-form').trigger('submit.prevent');
    await flushPromises();
    expect(adminAPI.groups.update).toHaveBeenCalledWith(group.id, expect.objectContaining({
      codex_models_manifest_config: { enabled: true, account_ids: [17], fallback_to_scheduler: false }
    }));
    const saved = { ...group, codex_models_manifest_config: { enabled: true, account_ids: [17], fallback_to_scheduler: false } };
    wrapper.unmount();
    listGroups.mockResolvedValue({ items: [saved], total: 1, page: 1, page_size: 20, pages: 1 });
    const reopened = mountView();
    await flushPromises();
    await reopened.findAll('button').find(button => button.text().includes('common.edit'))!.trigger('click');
    await flushPromises();
    expect(reopened.get('[data-testid="codex-manifest-value"]').text()).toBe(JSON.stringify(saved.codex_models_manifest_config));
    reopened.unmount();
  });

  it("preserves consecutive child updates on the reactive edit config", async () => {
    const wrapper = mountView();
    await flushPromises();

    const editButton = wrapper
      .findAll("button")
      .find((button) => button.text().includes("common.edit"));
    expect(editButton).toBeTruthy();
    await editButton!.trigger("click");
    await flushPromises();

    expect(wrapper.get('[data-testid="codex-manifest-value"]').text()).toBe(
      JSON.stringify(sourceGroup.codex_models_manifest_config),
    );

    await wrapper.get('[data-testid="codex-manifest-enable"]').trigger("click");
    await flushPromises();
    expect(wrapper.get('[data-testid="codex-manifest-value"]').text()).toContain(
      '"enabled":true',
    );

    await wrapper
      .get('[data-testid="codex-manifest-select-account"]')
      .trigger("click");
    await flushPromises();
    expect(wrapper.get('[data-testid="codex-manifest-value"]').text()).toBe(
      JSON.stringify({
        enabled: true,
        account_ids: [17],
        fallback_to_scheduler: false,
      }),
    );

    wrapper.unmount();
  });
});
