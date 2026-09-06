export namespace endpoint {

	export class UpstreamGroupSnapshot {
	    id: number;
	    name: string;
	    members: string[];

	    static createFrom(source: any = {}) {
	        return new UpstreamGroupSnapshot(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.members = source["members"];
	    }
	}

}

export namespace logging {

	export class LogEntry {
	    timestamp: string;
	    level: string;
	    message: string;
	    attrs: Record<string, string>;

	    static createFrom(source: any = {}) {
	        return new LogEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.timestamp = source["timestamp"];
	        this.level = source["level"];
	        this.message = source["message"];
	        this.attrs = source["attrs"];
	    }
	}

}

export namespace main {

	export class AccountScheduleCandidateDecisionInfo {
	    account_id: number;
	    account_name: string;
	    provider_type: string;
	    priority: number;
	    tier_index: number;
	    tier_label: string;
	    quota_status: string;
	    effective_quota_remaining?: number;
	    fail_count: number;
	    last_success_at: string;
	    decision: string;
	    reason: string;
	    reason_detail: string;
	    runtime_outcome?: string;
	    runtime_error?: string;

	    static createFrom(source: any = {}) {
	        return new AccountScheduleCandidateDecisionInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.account_id = source["account_id"];
	        this.account_name = source["account_name"];
	        this.provider_type = source["provider_type"];
	        this.priority = source["priority"];
	        this.tier_index = source["tier_index"];
	        this.tier_label = source["tier_label"];
	        this.quota_status = source["quota_status"];
	        this.effective_quota_remaining = source["effective_quota_remaining"];
	        this.fail_count = source["fail_count"];
	        this.last_success_at = source["last_success_at"];
	        this.decision = source["decision"];
	        this.reason = source["reason"];
	        this.reason_detail = source["reason_detail"];
	        this.runtime_outcome = source["runtime_outcome"];
	        this.runtime_error = source["runtime_error"];
	    }
	}
	export class BatchHealthCheckResult {
	    success: boolean;
	    message: string;
	    total: number;
	    healthy_count: number;
	    unhealthy_count: number;

	    static createFrom(source: any = {}) {
	        return new BatchHealthCheckResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.total = source["total"];
	        this.healthy_count = source["healthy_count"];
	        this.unhealthy_count = source["unhealthy_count"];
	    }
	}
	export class UpdateSettingInput {
	    category: string;
	    key: string;
	    value: string;

	    static createFrom(source: any = {}) {
	        return new UpdateSettingInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.category = source["category"];
	        this.key = source["key"];
	        this.value = source["value"];
	    }
	}
	export class BatchUpdateSettingsInput {
	    settings: UpdateSettingInput[];

	    static createFrom(source: any = {}) {
	        return new BatchUpdateSettingsInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.settings = this.convertValues(source["settings"], UpdateSettingInput);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CategoryInfo {
	    name: string;
	    label: string;
	    description: string;
	    icon: string;
	    order: number;

	    static createFrom(source: any = {}) {
	        return new CategoryInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.label = source["label"];
	        this.description = source["description"];
	        this.icon = source["icon"];
	        this.order = source["order"];
	    }
	}
	export class ChartDataPoint {
	    time: string;
	    total: number;
	    success: number;
	    fail: number;
	    avg: number;
	    min: number;
	    max: number;
	    value: number;
	    timestamp: string;

	    static createFrom(source: any = {}) {
	        return new ChartDataPoint(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = source["time"];
	        this.total = source["total"];
	        this.success = source["success"];
	        this.fail = source["fail"];
	        this.avg = source["avg"];
	        this.min = source["min"];
	        this.max = source["max"];
	        this.value = source["value"];
	        this.timestamp = source["timestamp"];
	    }
	}
	export class ClaudeRoutingState {
	    mode: string;
	    endpoint_name: string;
	    set_by: string;
	    set_at: string;
	    fallback_enabled: boolean;
	    revision: number;
	    fallback_reason: string;
	    last_effective_endpoint: string;
	    last_decision_at: string;
	    available_endpoints: string[];

	    static createFrom(source: any = {}) {
	        return new ClaudeRoutingState(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.endpoint_name = source["endpoint_name"];
	        this.set_by = source["set_by"];
	        this.set_at = source["set_at"];
	        this.fallback_enabled = source["fallback_enabled"];
	        this.revision = source["revision"];
	        this.fallback_reason = source["fallback_reason"];
	        this.last_effective_endpoint = source["last_effective_endpoint"];
	        this.last_decision_at = source["last_decision_at"];
	        this.available_endpoints = source["available_endpoints"];
	    }
	}
	export class CodexModelEntryInfo {
	    id: string;
	    object: string;
	    owned_by: string;
	    source: string;
	    enabled: boolean;
	    deprecated: boolean;
	    display_name: string;
	    description: string;

	    static createFrom(source: any = {}) {
	        return new CodexModelEntryInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.object = source["object"];
	        this.owned_by = source["owned_by"];
	        this.source = source["source"];
	        this.enabled = source["enabled"];
	        this.deprecated = source["deprecated"];
	        this.display_name = source["display_name"];
	        this.description = source["description"];
	    }
	}
	export class CodexModelCatalogInfo {
	    enabled: boolean;
	    mode: string;
	    models: CodexModelEntryInfo[];
	    effective_count: number;

	    static createFrom(source: any = {}) {
	        return new CodexModelCatalogInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.mode = source["mode"];
	        this.models = this.convertValues(source["models"], CodexModelEntryInfo);
	        this.effective_count = source["effective_count"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class CodexModelRouteBindingInfo {
	    assignment_id: number;
	    model: string;
	    account_ids: number[];
	    member_enabled: Record<number, boolean>;
	    route_mode: string;
	    route_target_account_id?: number;
	    enabled: boolean;
	    note: string;
	    last_account_id?: number;
	    last_outcome?: string;
	    last_decision_at?: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new CodexModelRouteBindingInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.assignment_id = source["assignment_id"];
	        this.model = source["model"];
	        this.account_ids = source["account_ids"];
	        this.member_enabled = source["member_enabled"];
	        this.route_mode = source["route_mode"];
	        this.route_target_account_id = source["route_target_account_id"];
	        this.enabled = source["enabled"];
	        this.note = source["note"];
	        this.last_account_id = source["last_account_id"];
	        this.last_outcome = source["last_outcome"];
	        this.last_decision_at = source["last_decision_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class CodexModelRouteBindingInput {
	    model: string;
	    account_ids: number[];
	    member_enabled: Record<number, boolean>;
	    route_mode: string;
	    route_target_account_id: number;
	    enabled: boolean;
	    note: string;

	    static createFrom(source: any = {}) {
	        return new CodexModelRouteBindingInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.account_ids = source["account_ids"];
	        this.member_enabled = source["member_enabled"];
	        this.route_mode = source["route_mode"];
	        this.route_target_account_id = source["route_target_account_id"];
	        this.enabled = source["enabled"];
	        this.note = source["note"];
	    }
	}
	export class CodexModelRoutingTestAccountInfo {
	    account_id: number;
	    account_name: string;
	    provider_type: string;
	    status: string;
	    detail: string;
	    decision: string;
	    reason: string;

	    static createFrom(source: any = {}) {
	        return new CodexModelRoutingTestAccountInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.account_id = source["account_id"];
	        this.account_name = source["account_name"];
	        this.provider_type = source["provider_type"];
	        this.status = source["status"];
	        this.detail = source["detail"];
	        this.decision = source["decision"];
	        this.reason = source["reason"];
	    }
	}
	export class CodexModelRoutingTestInput {
	    model: string;

	    static createFrom(source: any = {}) {
	        return new CodexModelRoutingTestInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	    }
	}
	export class CodexModelRoutingTestResult {
	    model: string;
	    assigned: boolean;
	    scope_kind: string;
	    group_id?: number;
	    group_name?: string;
	    route_mode?: string;
	    route_target_account_id?: number;
	    unassigned_reason?: string;
	    accounts: CodexModelRoutingTestAccountInfo[];

	    static createFrom(source: any = {}) {
	        return new CodexModelRoutingTestResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.assigned = source["assigned"];
	        this.scope_kind = source["scope_kind"];
	        this.group_id = source["group_id"];
	        this.group_name = source["group_name"];
	        this.route_mode = source["route_mode"];
	        this.route_target_account_id = source["route_target_account_id"];
	        this.unassigned_reason = source["unassigned_reason"];
	        this.accounts = this.convertValues(source["accounts"], CodexModelRoutingTestAccountInfo);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigInfo {
	    timezone: string;
	    server_host: string;
	    server_port: number;
	    auth_enabled: boolean;
	    proxy_enabled: boolean;
	    tracking_enabled: boolean;
	    failover_enabled: boolean;

	    static createFrom(source: any = {}) {
	        return new ConfigInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.timezone = source["timezone"];
	        this.server_host = source["server_host"];
	        this.server_port = source["server_port"];
	        this.auth_enabled = source["auth_enabled"];
	        this.proxy_enabled = source["proxy_enabled"];
	        this.tracking_enabled = source["tracking_enabled"];
	        this.failover_enabled = source["failover_enabled"];
	    }
	}
	export class CreateEndpointInput {
	    name: string;
	    url: string;
	    token: string;
	    api_key: string;
	    clear_token: boolean;
	    clear_api_key: boolean;
	    headers: Record<string, string>;
	    priority: number;
	    failover_enabled: boolean;
	    availability_enabled?: boolean;
	    cooldown_seconds?: number;
	    timeout_seconds: number;
	    supports_count_tokens: boolean;
	    model_rewrite_rules: string;
	    cost_multiplier: number;
	    input_cost_multiplier: number;
	    output_cost_multiplier: number;
	    cache_creation_cost_multiplier: number;
	    cache_creation_cost_multiplier_1h: number;
	    cache_read_cost_multiplier: number;

	    static createFrom(source: any = {}) {
	        return new CreateEndpointInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.url = source["url"];
	        this.token = source["token"];
	        this.api_key = source["api_key"];
	        this.clear_token = source["clear_token"];
	        this.clear_api_key = source["clear_api_key"];
	        this.headers = source["headers"];
	        this.priority = source["priority"];
	        this.failover_enabled = source["failover_enabled"];
	        this.availability_enabled = source["availability_enabled"];
	        this.cooldown_seconds = source["cooldown_seconds"];
	        this.timeout_seconds = source["timeout_seconds"];
	        this.supports_count_tokens = source["supports_count_tokens"];
	        this.model_rewrite_rules = source["model_rewrite_rules"];
	        this.cost_multiplier = source["cost_multiplier"];
	        this.input_cost_multiplier = source["input_cost_multiplier"];
	        this.output_cost_multiplier = source["output_cost_multiplier"];
	        this.cache_creation_cost_multiplier = source["cache_creation_cost_multiplier"];
	        this.cache_creation_cost_multiplier_1h = source["cache_creation_cost_multiplier_1h"];
	        this.cache_read_cost_multiplier = source["cache_read_cost_multiplier"];
	    }
	}
	export class CreateModelPricingInput {
	    model_name: string;
	    display_name: string;
	    description: string;
	    input_price: number;
	    output_price: number;
	    cache_creation_price_5m: number;
	    cache_creation_price_1h: number;
	    cache_read_price: number;
	    is_default: boolean;

	    static createFrom(source: any = {}) {
	        return new CreateModelPricingInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model_name = source["model_name"];
	        this.display_name = source["display_name"];
	        this.description = source["description"];
	        this.input_price = source["input_price"];
	        this.output_price = source["output_price"];
	        this.cache_creation_price_5m = source["cache_creation_price_5m"];
	        this.cache_creation_price_1h = source["cache_creation_price_1h"];
	        this.cache_read_price = source["cache_read_price"];
	        this.is_default = source["is_default"];
	    }
	}
	export class CreateUpstreamAccountInput {
	    provider_type: string;
	    account_name: string;
	    credential_raw: string;
	    base_url: string;
	    model_rewrite_rules: string;
	    enable_request_compression: boolean;
	    strip_codex_internal_metadata?: boolean;
	    cost_multiplier: number;
	    input_cost_multiplier: number;
	    output_cost_multiplier: number;
	    cache_creation_cost_multiplier: number;
	    cache_creation_cost_multiplier_1h: number;
	    cache_read_cost_multiplier: number;
	    group_key: string;
	    priority: number;
	    enabled: boolean;

	    static createFrom(source: any = {}) {
	        return new CreateUpstreamAccountInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider_type = source["provider_type"];
	        this.account_name = source["account_name"];
	        this.credential_raw = source["credential_raw"];
	        this.base_url = source["base_url"];
	        this.model_rewrite_rules = source["model_rewrite_rules"];
	        this.enable_request_compression = source["enable_request_compression"];
	        this.strip_codex_internal_metadata = source["strip_codex_internal_metadata"];
	        this.cost_multiplier = source["cost_multiplier"];
	        this.input_cost_multiplier = source["input_cost_multiplier"];
	        this.output_cost_multiplier = source["output_cost_multiplier"];
	        this.cache_creation_cost_multiplier = source["cache_creation_cost_multiplier"];
	        this.cache_creation_cost_multiplier_1h = source["cache_creation_cost_multiplier_1h"];
	        this.cache_read_cost_multiplier = source["cache_read_cost_multiplier"];
	        this.group_key = source["group_key"];
	        this.priority = source["priority"];
	        this.enabled = source["enabled"];
	    }
	}
	export class EnableAutomaticAccountSelectionResult {
	    success: boolean;
	    changed: boolean;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new EnableAutomaticAccountSelectionResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.changed = source["changed"];
	        this.message = source["message"];
	    }
	}
	export class EndpointCostItem {
	    name: string;
	    request_family: string;
	    tokens: number;
	    cost: number;

	    static createFrom(source: any = {}) {
	        return new EndpointCostItem(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.request_family = source["request_family"];
	        this.tokens = source["tokens"];
	        this.cost = source["cost"];
	    }
	}
	export class EndpointHealthData {
	    healthy: number;
	    unhealthy: number;
	    total: number;

	    static createFrom(source: any = {}) {
	        return new EndpointHealthData(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.healthy = source["healthy"];
	        this.unhealthy = source["unhealthy"];
	        this.total = source["total"];
	    }
	}
	export class EndpointInfo {
	    name: string;
	    url: string;
	    priority: number;
	    healthy: boolean;
	    last_check: string;
	    response_time_ms: number;
	    consecutive_fail: number;

	    static createFrom(source: any = {}) {
	        return new EndpointInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.url = source["url"];
	        this.priority = source["priority"];
	        this.healthy = source["healthy"];
	        this.last_check = source["last_check"];
	        this.response_time_ms = source["response_time_ms"];
	        this.consecutive_fail = source["consecutive_fail"];
	    }
	}
	export class EndpointRecordInfo {
	    id: number;
	    name: string;
	    url: string;
	    token_masked: string;
	    api_key_masked: string;
	    headers: Record<string, string>;
	    priority: number;
	    failover_enabled: boolean;
	    cooldown_seconds?: number;
	    timeout_seconds: number;
	    supports_count_tokens: boolean;
	    model_rewrite_rules: string;
	    cost_multiplier: number;
	    input_cost_multiplier: number;
	    output_cost_multiplier: number;
	    cache_creation_cost_multiplier: number;
	    cache_creation_cost_multiplier_1h: number;
	    cache_read_cost_multiplier: number;
	    availability_enabled: boolean;
	    created_at: string;
	    updated_at: string;
	    healthy: boolean;
	    never_checked: boolean;
	    last_check: string;
	    response_time_ms: number;
	    in_cooldown: boolean;
	    cooldown_until: string;
	    cooldown_reason: string;
	    cooldown_persist_pending: boolean;

	    static createFrom(source: any = {}) {
	        return new EndpointRecordInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.url = source["url"];
	        this.token_masked = source["token_masked"];
	        this.api_key_masked = source["api_key_masked"];
	        this.headers = source["headers"];
	        this.priority = source["priority"];
	        this.failover_enabled = source["failover_enabled"];
	        this.cooldown_seconds = source["cooldown_seconds"];
	        this.timeout_seconds = source["timeout_seconds"];
	        this.supports_count_tokens = source["supports_count_tokens"];
	        this.model_rewrite_rules = source["model_rewrite_rules"];
	        this.cost_multiplier = source["cost_multiplier"];
	        this.input_cost_multiplier = source["input_cost_multiplier"];
	        this.output_cost_multiplier = source["output_cost_multiplier"];
	        this.cache_creation_cost_multiplier = source["cache_creation_cost_multiplier"];
	        this.cache_creation_cost_multiplier_1h = source["cache_creation_cost_multiplier_1h"];
	        this.cache_read_cost_multiplier = source["cache_read_cost_multiplier"];
	        this.availability_enabled = source["availability_enabled"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	        this.healthy = source["healthy"];
	        this.never_checked = source["never_checked"];
	        this.last_check = source["last_check"];
	        this.response_time_ms = source["response_time_ms"];
	        this.in_cooldown = source["in_cooldown"];
	        this.cooldown_until = source["cooldown_until"];
	        this.cooldown_reason = source["cooldown_reason"];
	        this.cooldown_persist_pending = source["cooldown_persist_pending"];
	    }
	}
	export class EndpointScheduleDecisionInfo {
	    name: string;
	    decision: string;
	    reason: string;
	    available_at: string;
	    runtime_outcome?: string;
	    runtime_error?: string;

	    static createFrom(source: any = {}) {
	        return new EndpointScheduleDecisionInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.decision = source["decision"];
	        this.reason = source["reason"];
	        this.available_at = source["available_at"];
	        this.runtime_outcome = source["runtime_outcome"];
	        this.runtime_error = source["runtime_error"];
	    }
	}
	export class EndpointStorageStatus {
	    enabled: boolean;
	    storage_type: string;
	    total_count: number;
	    available_count: number;

	    static createFrom(source: any = {}) {
	        return new EndpointStorageStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.storage_type = source["storage_type"];
	        this.total_count = source["total_count"];
	        this.available_count = source["available_count"];
	    }
	}
	export class ExchangeChatGPTOAuthCallbackInput {
	    session_id: string;
	    callback_url: string;

	    static createFrom(source: any = {}) {
	        return new ExchangeChatGPTOAuthCallbackInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.session_id = source["session_id"];
	        this.callback_url = source["callback_url"];
	    }
	}
	export class ExchangeChatGPTOAuthCallbackResult {
	    success: boolean;
	    refresh_token?: string;
	    access_token?: string;
	    id_token?: string;
	    expires_at?: string;
	    plan_type?: string;
	    chatgpt_account_id?: string;
	    chatgpt_user_id?: string;
	    organization_id?: string;
	    credential_raw?: string;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new ExchangeChatGPTOAuthCallbackResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.refresh_token = source["refresh_token"];
	        this.access_token = source["access_token"];
	        this.id_token = source["id_token"];
	        this.expires_at = source["expires_at"];
	        this.plan_type = source["plan_type"];
	        this.chatgpt_account_id = source["chatgpt_account_id"];
	        this.chatgpt_user_id = source["chatgpt_user_id"];
	        this.organization_id = source["organization_id"];
	        this.credential_raw = source["credential_raw"];
	        this.message = source["message"];
	    }
	}
	export class GenerateChatGPTOAuthLinkResult {
	    session_id: string;
	    auth_url: string;
	    redirect_uri: string;
	    expires_at: string;

	    static createFrom(source: any = {}) {
	        return new GenerateChatGPTOAuthLinkResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.session_id = source["session_id"];
	        this.auth_url = source["auth_url"];
	        this.redirect_uri = source["redirect_uri"];
	        this.expires_at = source["expires_at"];
	    }
	}
	export class ImportPrivacySecretCandidateInput {
	    source_type: string;
	    source_ref: string;
	    name: string;
	    category: string;
	    placeholder: string;
	    description: string;
	    secret_value: string;

	    static createFrom(source: any = {}) {
	        return new ImportPrivacySecretCandidateInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source_type = source["source_type"];
	        this.source_ref = source["source_ref"];
	        this.name = source["name"];
	        this.category = source["category"];
	        this.placeholder = source["placeholder"];
	        this.description = source["description"];
	        this.secret_value = source["secret_value"];
	    }
	}
	export class LatestAccountScheduleSnapshotInfo {
	    has_snapshot: boolean;
	    request_id?: string;
	    captured_at: string;
	    updated_at: string;
	    request_path: string;
	    selected_priority: number;
	    selected_tier_index: number;
	    selected_tier_label: string;
	    degraded_to_lower_priority: boolean;
	    selected_account_id: number;
	    selected_account_name: string;
	    final_outcome: string;
	    final_error: string;
	    summary: string;
	    request_model?: string;
	    model_routing_family?: string;
	    model_routing_version?: number;
	    route_scope_kind?: string;
	    route_scope_group_id?: number;
	    route_scope_group_name?: string;
	    route_mode?: string;
	    route_target_account_id?: number;
	    candidates: AccountScheduleCandidateDecisionInfo[];

	    static createFrom(source: any = {}) {
	        return new LatestAccountScheduleSnapshotInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.has_snapshot = source["has_snapshot"];
	        this.request_id = source["request_id"];
	        this.captured_at = source["captured_at"];
	        this.updated_at = source["updated_at"];
	        this.request_path = source["request_path"];
	        this.selected_priority = source["selected_priority"];
	        this.selected_tier_index = source["selected_tier_index"];
	        this.selected_tier_label = source["selected_tier_label"];
	        this.degraded_to_lower_priority = source["degraded_to_lower_priority"];
	        this.selected_account_id = source["selected_account_id"];
	        this.selected_account_name = source["selected_account_name"];
	        this.final_outcome = source["final_outcome"];
	        this.final_error = source["final_error"];
	        this.summary = source["summary"];
	        this.request_model = source["request_model"];
	        this.model_routing_family = source["model_routing_family"];
	        this.model_routing_version = source["model_routing_version"];
	        this.route_scope_kind = source["route_scope_kind"];
	        this.route_scope_group_id = source["route_scope_group_id"];
	        this.route_scope_group_name = source["route_scope_group_name"];
	        this.route_mode = source["route_mode"];
	        this.route_target_account_id = source["route_target_account_id"];
	        this.candidates = this.convertValues(source["candidates"], AccountScheduleCandidateDecisionInfo);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LatestEndpointScheduleSnapshotInfo {
	    has_snapshot: boolean;
	    request_id?: string;
	    captured_at: string;
	    updated_at: string;
	    request_path: string;
	    selected_endpoint: string;
	    route_mode: string;
	    route_endpoint_name: string;
	    route_fallback_enabled: boolean;
	    route_scope_kind?: string;
	    route_scope?: string;
	    route_scope_group_id?: number;
	    route_scope_group_name?: string;
	    upstream_group?: endpoint.UpstreamGroupSnapshot;
	    request_model?: string;
	    model_routing_version?: number;
	    failover_enabled: boolean;
	    final_outcome: string;
	    final_error: string;
	    summary: string;
	    decisions: EndpointScheduleDecisionInfo[];

	    static createFrom(source: any = {}) {
	        return new LatestEndpointScheduleSnapshotInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.has_snapshot = source["has_snapshot"];
	        this.request_id = source["request_id"];
	        this.captured_at = source["captured_at"];
	        this.updated_at = source["updated_at"];
	        this.request_path = source["request_path"];
	        this.selected_endpoint = source["selected_endpoint"];
	        this.route_mode = source["route_mode"];
	        this.route_endpoint_name = source["route_endpoint_name"];
	        this.route_fallback_enabled = source["route_fallback_enabled"];
	        this.route_scope_kind = source["route_scope_kind"];
	        this.route_scope = source["route_scope"];
	        this.route_scope_group_id = source["route_scope_group_id"];
	        this.route_scope_group_name = source["route_scope_group_name"];
	        this.upstream_group = this.convertValues(source["upstream_group"], endpoint.UpstreamGroupSnapshot);
	        this.request_model = source["request_model"];
	        this.model_routing_version = source["model_routing_version"];
	        this.failover_enabled = source["failover_enabled"];
	        this.final_outcome = source["final_outcome"];
	        this.final_error = source["final_error"];
	        this.summary = source["summary"];
	        this.decisions = this.convertValues(source["decisions"], EndpointScheduleDecisionInfo);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ModelPricingInfo {
	    id: number;
	    model_name: string;
	    display_name: string;
	    description: string;
	    input_price: number;
	    output_price: number;
	    cache_creation_price_5m: number;
	    cache_creation_price_1h: number;
	    cache_read_price: number;
	    is_default: boolean;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new ModelPricingInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.model_name = source["model_name"];
	        this.display_name = source["display_name"];
	        this.description = source["description"];
	        this.input_price = source["input_price"];
	        this.output_price = source["output_price"];
	        this.cache_creation_price_5m = source["cache_creation_price_5m"];
	        this.cache_creation_price_1h = source["cache_creation_price_1h"];
	        this.cache_read_price = source["cache_read_price"];
	        this.is_default = source["is_default"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class ModelPricingStorageStatus {
	    enabled: boolean;
	    total_count: number;
	    has_default: boolean;

	    static createFrom(source: any = {}) {
	        return new ModelPricingStorageStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.total_count = source["total_count"];
	        this.has_default = source["has_default"];
	    }
	}
	export class ModelRoutingMemberInfo {
	    name: string;
	    exists: boolean;
	    priority: number;
	    availability_enabled: boolean;
	    auto_schedule_enabled: boolean;
	    healthy: boolean;
	    cooldown_until?: string;
	    cooldown_reason?: string;
	    detail?: string;
	    status: string;

	    static createFrom(source: any = {}) {
	        return new ModelRoutingMemberInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.exists = source["exists"];
	        this.priority = source["priority"];
	        this.availability_enabled = source["availability_enabled"];
	        this.auto_schedule_enabled = source["auto_schedule_enabled"];
	        this.healthy = source["healthy"];
	        this.cooldown_until = source["cooldown_until"];
	        this.cooldown_reason = source["cooldown_reason"];
	        this.detail = source["detail"];
	        this.status = source["status"];
	    }
	}
	export class ModelRouteBindingInfo {
	    assignment_id: number;
	    model: string;
	    endpoints: ModelRoutingMemberInfo[];
	    route_mode: string;
	    route_target: string;
	    enabled: boolean;
	    note: string;
	    shared_models: string[];
	    last_effective_endpoint?: string;
	    last_decision_at?: string;
	    last_outcome?: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new ModelRouteBindingInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.assignment_id = source["assignment_id"];
	        this.model = source["model"];
	        this.endpoints = this.convertValues(source["endpoints"], ModelRoutingMemberInfo);
	        this.route_mode = source["route_mode"];
	        this.route_target = source["route_target"];
	        this.enabled = source["enabled"];
	        this.note = source["note"];
	        this.shared_models = source["shared_models"];
	        this.last_effective_endpoint = source["last_effective_endpoint"];
	        this.last_decision_at = source["last_decision_at"];
	        this.last_outcome = source["last_outcome"];
	        this.updated_at = source["updated_at"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ModelRouteBindingInput {
	    model: string;
	    endpoints: string[];
	    route_mode: string;
	    route_target: string;
	    enabled: boolean;
	    note: string;

	    static createFrom(source: any = {}) {
	        return new ModelRouteBindingInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.endpoints = source["endpoints"];
	        this.route_mode = source["route_mode"];
	        this.route_target = source["route_target"];
	        this.enabled = source["enabled"];
	        this.note = source["note"];
	    }
	}

	export class ModelRoutingSettingsInfo {
	    claude_enabled: boolean;
	    codex_enabled: boolean;
	    status: string;
	    version: number;
	    updated_at: string;
	    binding_count: number;
	    claude_status: string;
	    codex_status: string;
	    claude_version: number;
	    codex_version: number;
	    claude_binding_count: number;
	    codex_binding_count: number;

	    static createFrom(source: any = {}) {
	        return new ModelRoutingSettingsInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.claude_enabled = source["claude_enabled"];
	        this.codex_enabled = source["codex_enabled"];
	        this.status = source["status"];
	        this.version = source["version"];
	        this.updated_at = source["updated_at"];
	        this.binding_count = source["binding_count"];
	        this.claude_status = source["claude_status"];
	        this.codex_status = source["codex_status"];
	        this.claude_version = source["claude_version"];
	        this.codex_version = source["codex_version"];
	        this.claude_binding_count = source["claude_binding_count"];
	        this.codex_binding_count = source["codex_binding_count"];
	    }
	}
	export class ModelRoutingTestInput {
	    model: string;

	    static createFrom(source: any = {}) {
	        return new ModelRoutingTestInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	    }
	}
	export class ModelRoutingTestResult {
	    model: string;
	    assigned: boolean;
	    scope_kind: string;
	    group_id?: number;
	    group_name?: string;
	    route_mode?: string;
	    route_target?: string;
	    unassigned_reason?: string;
	    members: ModelRoutingMemberInfo[];

	    static createFrom(source: any = {}) {
	        return new ModelRoutingTestResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.assigned = source["assigned"];
	        this.scope_kind = source["scope_kind"];
	        this.group_id = source["group_id"];
	        this.group_name = source["group_name"];
	        this.route_mode = source["route_mode"];
	        this.route_target = source["route_target"];
	        this.unassigned_reason = source["unassigned_reason"];
	        this.members = this.convertValues(source["members"], ModelRoutingMemberInfo);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MoveUpstreamAccountToTierResult {
	    success: boolean;
	    changed: boolean;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new MoveUpstreamAccountToTierResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.changed = source["changed"];
	        this.message = source["message"];
	    }
	}
	export class PinUpstreamAccountSelectionResult {
	    success: boolean;
	    changed: boolean;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new PinUpstreamAccountSelectionResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.changed = source["changed"];
	        this.message = source["message"];
	    }
	}
	export class PortInfo {
	    preferred_port: number;
	    actual_port: number;
	    is_default: boolean;
	    was_occupied: boolean;

	    static createFrom(source: any = {}) {
	        return new PortInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.preferred_port = source["preferred_port"];
	        this.actual_port = source["actual_port"];
	        this.is_default = source["is_default"];
	        this.was_occupied = source["was_occupied"];
	    }
	}
	export class PrivacyExactSecretInfo {
	    id: number;
	    enabled: boolean;
	    name: string;
	    category: string;
	    placeholder: string;
	    source_type: string;
	    source_ref: string;
	    description: string;
	    masked_value: string;
	    value_length: number;
	    value_hash_short: string;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new PrivacyExactSecretInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.enabled = source["enabled"];
	        this.name = source["name"];
	        this.category = source["category"];
	        this.placeholder = source["placeholder"];
	        this.source_type = source["source_type"];
	        this.source_ref = source["source_ref"];
	        this.description = source["description"];
	        this.masked_value = source["masked_value"];
	        this.value_length = source["value_length"];
	        this.value_hash_short = source["value_hash_short"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class PrivacyExactSecretInput {
	    enabled: boolean;
	    name: string;
	    secret_value: string;
	    category: string;
	    placeholder: string;
	    source_type: string;
	    source_ref: string;
	    description: string;

	    static createFrom(source: any = {}) {
	        return new PrivacyExactSecretInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.name = source["name"];
	        this.secret_value = source["secret_value"];
	        this.category = source["category"];
	        this.placeholder = source["placeholder"];
	        this.source_type = source["source_type"];
	        this.source_ref = source["source_ref"];
	        this.description = source["description"];
	    }
	}
	export class PrivacyPresetInfo {
	    id: string;
	    name: string;
	    description: string;
	    rule_count: number;

	    static createFrom(source: any = {}) {
	        return new PrivacyPresetInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.rule_count = source["rule_count"];
	    }
	}
	export class PrivacyRuleInfo {
	    id: number;
	    enabled: boolean;
	    name: string;
	    description: string;
	    priority: number;
	    match_type: string;
	    pattern: string;
	    placeholder: string;
	    action: string;
	    scope_json: string;
	    source: string;
	    compile_error: string;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new PrivacyRuleInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.enabled = source["enabled"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.priority = source["priority"];
	        this.match_type = source["match_type"];
	        this.pattern = source["pattern"];
	        this.placeholder = source["placeholder"];
	        this.action = source["action"];
	        this.scope_json = source["scope_json"];
	        this.source = source["source"];
	        this.compile_error = source["compile_error"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class PrivacySettingsInfo {
	    mode: string;
	    scan_max_bytes: number;
	    over_limit_action: string;
	    on_error: string;
	    version: number;
	    status: string;
	    compile_error: string;
	    enabled_rules: number;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new PrivacySettingsInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.scan_max_bytes = source["scan_max_bytes"];
	        this.over_limit_action = source["over_limit_action"];
	        this.on_error = source["on_error"];
	        this.version = source["version"];
	        this.status = source["status"];
	        this.compile_error = source["compile_error"];
	        this.enabled_rules = source["enabled_rules"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class PrivacyRuleExportInfo {
	    exported_at: string;
	    settings: PrivacySettingsInfo;
	    rules: PrivacyRuleInfo[];

	    static createFrom(source: any = {}) {
	        return new PrivacyRuleExportInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.exported_at = source["exported_at"];
	        this.settings = this.convertValues(source["settings"], PrivacySettingsInfo);
	        this.rules = this.convertValues(source["rules"], PrivacyRuleInfo);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PrivacyRuleHitInfo {
	    rule_id: number;
	    rule_name: string;
	    source: string;
	    action: string;
	    count: number;

	    static createFrom(source: any = {}) {
	        return new PrivacyRuleHitInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rule_id = source["rule_id"];
	        this.rule_name = source["rule_name"];
	        this.source = source["source"];
	        this.action = source["action"];
	        this.count = source["count"];
	    }
	}

	export class PrivacyRuleInput {
	    enabled: boolean;
	    name: string;
	    description: string;
	    priority: number;
	    match_type: string;
	    pattern: string;
	    placeholder: string;
	    action: string;
	    scope_json: string;

	    static createFrom(source: any = {}) {
	        return new PrivacyRuleInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.priority = source["priority"];
	        this.match_type = source["match_type"];
	        this.pattern = source["pattern"];
	        this.placeholder = source["placeholder"];
	        this.action = source["action"];
	        this.scope_json = source["scope_json"];
	    }
	}
	export class PrivacyRuleOrderInput {
	    id: number;
	    priority: number;

	    static createFrom(source: any = {}) {
	        return new PrivacyRuleOrderInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.priority = source["priority"];
	    }
	}
	export class PrivacyRuleTestInput {
	    text: string;
	    path: string;
	    upstream_type: string;
	    endpoint_name: string;
	    account_id: number;
	    provider_type: string;

	    static createFrom(source: any = {}) {
	        return new PrivacyRuleTestInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.text = source["text"];
	        this.path = source["path"];
	        this.upstream_type = source["upstream_type"];
	        this.endpoint_name = source["endpoint_name"];
	        this.account_id = source["account_id"];
	        this.provider_type = source["provider_type"];
	    }
	}
	export class PrivacyRuleTestResult {
	    original_length: number;
	    hit_count: number;
	    changed: boolean;
	    replaced_text: string;
	    rule_hits: PrivacyRuleHitInfo[];
	    scan_duration_ms: number;

	    static createFrom(source: any = {}) {
	        return new PrivacyRuleTestResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.original_length = source["original_length"];
	        this.hit_count = source["hit_count"];
	        this.changed = source["changed"];
	        this.replaced_text = source["replaced_text"];
	        this.rule_hits = this.convertValues(source["rule_hits"], PrivacyRuleHitInfo);
	        this.scan_duration_ms = source["scan_duration_ms"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PrivacyRuntimeStatsInfo {
	    scan_count: number;
	    hit_count: number;
	    blocked_count: number;
	    truncated_count: number;
	    rule_stats: PrivacyRuleHitInfo[];

	    static createFrom(source: any = {}) {
	        return new PrivacyRuntimeStatsInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.scan_count = source["scan_count"];
	        this.hit_count = source["hit_count"];
	        this.blocked_count = source["blocked_count"];
	        this.truncated_count = source["truncated_count"];
	        this.rule_stats = this.convertValues(source["rule_stats"], PrivacyRuleHitInfo);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PrivacySecretImportCandidateInfo {
	    source_type: string;
	    source_ref: string;
	    name: string;
	    category: string;
	    masked_value: string;
	    value_length: number;
	    value_hash_short: string;
	    already_exists: boolean;

	    static createFrom(source: any = {}) {
	        return new PrivacySecretImportCandidateInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source_type = source["source_type"];
	        this.source_ref = source["source_ref"];
	        this.name = source["name"];
	        this.category = source["category"];
	        this.masked_value = source["masked_value"];
	        this.value_length = source["value_length"];
	        this.value_hash_short = source["value_hash_short"];
	        this.already_exists = source["already_exists"];
	    }
	}

	export class PrivacySettingsInput {
	    mode: string;
	    scan_max_bytes: number;
	    over_limit_action: string;
	    on_error: string;

	    static createFrom(source: any = {}) {
	        return new PrivacySettingsInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.scan_max_bytes = source["scan_max_bytes"];
	        this.over_limit_action = source["over_limit_action"];
	        this.on_error = source["on_error"];
	    }
	}
	export class RefreshUpstreamAccountProfileResult {
	    success: boolean;
	    message: string;
	    quota_status?: string;

	    static createFrom(source: any = {}) {
	        return new RefreshUpstreamAccountProfileResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.quota_status = source["quota_status"];
	    }
	}
	export class RequestRecord {
	    id: string;
	    request_id: string;
	    timestamp: string;
	    request_family: string;
	    endpoint: string;
	    model: string;
	    status: string;
	    http_status: number;
	    retry_count: number;
	    failure_reason?: string;
	    cancel_reason?: string;
	    upstream_type: string;
	    upstream_source_name: string;
	    upstream_name: string;
	    upstream_id: number;
	    route_mode?: string;
	    requested_endpoint?: string;
	    effective_endpoint?: string;
	    fallback_reason?: string;
	    route_decision_at?: string;
	    input_tokens: number;
	    output_tokens: number;
	    cache_creation_tokens: number;
	    cache_creation_5m_tokens: number;
	    cache_creation_1h_tokens: number;
	    cache_read_tokens: number;
	    response_time: number;
	    first_token_ms?: number;
	    completion_ms?: number;
	    is_streaming: boolean;
	    cost: number;

	    static createFrom(source: any = {}) {
	        return new RequestRecord(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.request_id = source["request_id"];
	        this.timestamp = source["timestamp"];
	        this.request_family = source["request_family"];
	        this.endpoint = source["endpoint"];
	        this.model = source["model"];
	        this.status = source["status"];
	        this.http_status = source["http_status"];
	        this.retry_count = source["retry_count"];
	        this.failure_reason = source["failure_reason"];
	        this.cancel_reason = source["cancel_reason"];
	        this.upstream_type = source["upstream_type"];
	        this.upstream_source_name = source["upstream_source_name"];
	        this.upstream_name = source["upstream_name"];
	        this.upstream_id = source["upstream_id"];
	        this.route_mode = source["route_mode"];
	        this.requested_endpoint = source["requested_endpoint"];
	        this.effective_endpoint = source["effective_endpoint"];
	        this.fallback_reason = source["fallback_reason"];
	        this.route_decision_at = source["route_decision_at"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cache_creation_tokens = source["cache_creation_tokens"];
	        this.cache_creation_5m_tokens = source["cache_creation_5m_tokens"];
	        this.cache_creation_1h_tokens = source["cache_creation_1h_tokens"];
	        this.cache_read_tokens = source["cache_read_tokens"];
	        this.response_time = source["response_time"];
	        this.first_token_ms = source["first_token_ms"];
	        this.completion_ms = source["completion_ms"];
	        this.is_streaming = source["is_streaming"];
	        this.cost = source["cost"];
	    }
	}
	export class RequestLifecycleDetail {
	    found: boolean;
	    source?: string;
	    request?: RequestRecord;
	    upstream_write_ms?: number;
	    schedule_snapshot_json?: string;
	    privacy_scan_json?: string;

	    static createFrom(source: any = {}) {
	        return new RequestLifecycleDetail(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.found = source["found"];
	        this.source = source["source"];
	        this.request = this.convertValues(source["request"], RequestRecord);
	        this.upstream_write_ms = source["upstream_write_ms"];
	        this.schedule_snapshot_json = source["schedule_snapshot_json"];
	        this.privacy_scan_json = source["privacy_scan_json"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RequestListResult {
	    requests: RequestRecord[];
	    total: number;
	    page: number;
	    page_size: number;

	    static createFrom(source: any = {}) {
	        return new RequestListResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requests = this.convertValues(source["requests"], RequestRecord);
	        this.total = source["total"];
	        this.page = source["page"];
	        this.page_size = source["page_size"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RequestQueryParams {
	    page: number;
	    page_size: number;
	    start_date: string;
	    end_date: string;
	    status: string;
	    model: string;
	    request_family: string;
	    upstream_name: string;
	    source_view: string;

	    static createFrom(source: any = {}) {
	        return new RequestQueryParams(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.page = source["page"];
	        this.page_size = source["page_size"];
	        this.start_date = source["start_date"];
	        this.end_date = source["end_date"];
	        this.status = source["status"];
	        this.model = source["model"];
	        this.request_family = source["request_family"];
	        this.upstream_name = source["upstream_name"];
	        this.source_view = source["source_view"];
	    }
	}

	export class SaveCodexModelCatalogInput {
	    enabled: boolean;
	    mode: string;
	    models: CodexModelEntryInfo[];

	    static createFrom(source: any = {}) {
	        return new SaveCodexModelCatalogInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.mode = source["mode"];
	        this.models = this.convertValues(source["models"], CodexModelEntryInfo);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SetClaudeRoutingOverrideInput {
	    mode: string;
	    endpoint_name: string;
	    fallback_enabled: boolean;

	    static createFrom(source: any = {}) {
	        return new SetClaudeRoutingOverrideInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.endpoint_name = source["endpoint_name"];
	        this.fallback_enabled = source["fallback_enabled"];
	    }
	}
	export class SetGroupActiveAccountResult {
	    success: boolean;
	    changed: boolean;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new SetGroupActiveAccountResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.changed = source["changed"];
	        this.message = source["message"];
	    }
	}
	export class SettingInfo {
	    id: number;
	    category: string;
	    key: string;
	    value: string;
	    value_type: string;
	    label: string;
	    description: string;
	    display_order: number;
	    requires_restart: boolean;
	    secret_configured: boolean;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new SettingInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.category = source["category"];
	        this.key = source["key"];
	        this.value = source["value"];
	        this.value_type = source["value_type"];
	        this.label = source["label"];
	        this.description = source["description"];
	        this.display_order = source["display_order"];
	        this.requires_restart = source["requires_restart"];
	        this.secret_configured = source["secret_configured"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class SettingsStorageStatus {
	    enabled: boolean;
	    total_count: number;
	    category_count: number;
	    is_initialized: boolean;

	    static createFrom(source: any = {}) {
	        return new SettingsStorageStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.total_count = source["total_count"];
	        this.category_count = source["category_count"];
	        this.is_initialized = source["is_initialized"];
	    }
	}
	export class SwapUpstreamAccountGroupsResult {
	    success: boolean;
	    changed: boolean;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new SwapUpstreamAccountGroupsResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.changed = source["changed"];
	        this.message = source["message"];
	    }
	}
	export class SystemStatus {
	    version: string;
	    uptime: string;
	    uptime_seconds: number;
	    start_time: string;
	    proxy_port: number;
	    proxy_host: string;
	    proxy_running: boolean;
	    config_path: string;
	    auth_enabled: boolean;

	    static createFrom(source: any = {}) {
	        return new SystemStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.uptime = source["uptime"];
	        this.uptime_seconds = source["uptime_seconds"];
	        this.start_time = source["start_time"];
	        this.proxy_port = source["proxy_port"];
	        this.proxy_host = source["proxy_host"];
	        this.proxy_running = source["proxy_running"];
	        this.config_path = source["config_path"];
	        this.auth_enabled = source["auth_enabled"];
	    }
	}
	export class TestUpstreamAccountResult {
	    success: boolean;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new TestUpstreamAccountResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	    }
	}
	export class TokenUsageData {
	    input_tokens: number;
	    output_tokens: number;
	    cache_creation_tokens: number;
	    cache_read_tokens: number;
	    total_tokens: number;

	    static createFrom(source: any = {}) {
	        return new TokenUsageData(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cache_creation_tokens = source["cache_creation_tokens"];
	        this.cache_read_tokens = source["cache_read_tokens"];
	        this.total_tokens = source["total_tokens"];
	    }
	}
	export class UpdateModelRoutingSettingsInput {
	    claude_enabled?: boolean;
	    codex_enabled?: boolean;

	    static createFrom(source: any = {}) {
	        return new UpdateModelRoutingSettingsInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.claude_enabled = source["claude_enabled"];
	        this.codex_enabled = source["codex_enabled"];
	    }
	}

	export class UpstreamAccountCredentialInfo {
	    id: number;
	    credential_raw: string;
	    credential_raw_masked: string;
	    has_credential: boolean;

	    static createFrom(source: any = {}) {
	        return new UpstreamAccountCredentialInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.credential_raw = source["credential_raw"];
	        this.credential_raw_masked = source["credential_raw_masked"];
	        this.has_credential = source["has_credential"];
	    }
	}
	export class UpstreamAccountInfo {
	    id: number;
	    provider_type: string;
	    account_name: string;
	    credential_raw: string;
	    credential_raw_masked: string;
	    has_credential: boolean;
	    is_active_selection: boolean;
	    is_group_preferred: boolean;
	    base_url: string;
	    model_rewrite_rules: string;
	    enable_request_compression: boolean;
	    strip_codex_internal_metadata: boolean;
	    cost_multiplier: number;
	    input_cost_multiplier: number;
	    output_cost_multiplier: number;
	    cache_creation_cost_multiplier: number;
	    cache_creation_cost_multiplier_1h: number;
	    cache_read_cost_multiplier: number;
	    group_key: string;
	    priority: number;
	    enabled: boolean;
	    state: string;
	    cooldown_until: string;
	    fail_count: number;
	    last_success_at: string;
	    last_error: string;
	    plan_type: string;
	    chatgpt_account_id: string;
	    chatgpt_user_id: string;
	    organization_id: string;
	    quota_5h_used_percent?: number;
	    quota_5h_reset_at: string;
	    quota_weekly_used_percent?: number;
	    quota_weekly_reset_at: string;
	    quota_status: string;
	    quota_refreshed_at: string;
	    fingerprint: string;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new UpstreamAccountInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.provider_type = source["provider_type"];
	        this.account_name = source["account_name"];
	        this.credential_raw = source["credential_raw"];
	        this.credential_raw_masked = source["credential_raw_masked"];
	        this.has_credential = source["has_credential"];
	        this.is_active_selection = source["is_active_selection"];
	        this.is_group_preferred = source["is_group_preferred"];
	        this.base_url = source["base_url"];
	        this.model_rewrite_rules = source["model_rewrite_rules"];
	        this.enable_request_compression = source["enable_request_compression"];
	        this.strip_codex_internal_metadata = source["strip_codex_internal_metadata"];
	        this.cost_multiplier = source["cost_multiplier"];
	        this.input_cost_multiplier = source["input_cost_multiplier"];
	        this.output_cost_multiplier = source["output_cost_multiplier"];
	        this.cache_creation_cost_multiplier = source["cache_creation_cost_multiplier"];
	        this.cache_creation_cost_multiplier_1h = source["cache_creation_cost_multiplier_1h"];
	        this.cache_read_cost_multiplier = source["cache_read_cost_multiplier"];
	        this.group_key = source["group_key"];
	        this.priority = source["priority"];
	        this.enabled = source["enabled"];
	        this.state = source["state"];
	        this.cooldown_until = source["cooldown_until"];
	        this.fail_count = source["fail_count"];
	        this.last_success_at = source["last_success_at"];
	        this.last_error = source["last_error"];
	        this.plan_type = source["plan_type"];
	        this.chatgpt_account_id = source["chatgpt_account_id"];
	        this.chatgpt_user_id = source["chatgpt_user_id"];
	        this.organization_id = source["organization_id"];
	        this.quota_5h_used_percent = source["quota_5h_used_percent"];
	        this.quota_5h_reset_at = source["quota_5h_reset_at"];
	        this.quota_weekly_used_percent = source["quota_weekly_used_percent"];
	        this.quota_weekly_reset_at = source["quota_weekly_reset_at"];
	        this.quota_status = source["quota_status"];
	        this.quota_refreshed_at = source["quota_refreshed_at"];
	        this.fingerprint = source["fingerprint"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class UsageStatsData {
	    period: string;
	    total_requests: number;
	    success_rate: number;
	    avg_duration_ms: number;
	    total_cost_usd: number;
	    total_tokens: number;
	    failed_requests: number;

	    static createFrom(source: any = {}) {
	        return new UsageStatsData(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.period = source["period"];
	        this.total_requests = source["total_requests"];
	        this.success_rate = source["success_rate"];
	        this.avg_duration_ms = source["avg_duration_ms"];
	        this.total_cost_usd = source["total_cost_usd"];
	        this.total_tokens = source["total_tokens"];
	        this.failed_requests = source["failed_requests"];
	    }
	}
	export class UsageStatsQueryParams {
	    period: string;
	    start_date: string;
	    end_date: string;
	    status: string;
	    model: string;
	    request_family: string;
	    upstream_name: string;
	    source_view: string;

	    static createFrom(source: any = {}) {
	        return new UsageStatsQueryParams(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.period = source["period"];
	        this.start_date = source["start_date"];
	        this.end_date = source["end_date"];
	        this.status = source["status"];
	        this.model = source["model"];
	        this.request_family = source["request_family"];
	        this.upstream_name = source["upstream_name"];
	        this.source_view = source["source_view"];
	    }
	}
	export class UsageSummary {
	    total_requests: number;
	    all_time_total_requests: number;
	    today_requests: number;
	    success_requests: number;
	    failed_requests: number;
	    total_input_tokens: number;
	    total_output_tokens: number;
	    total_cost: number;
	    today_cost: number;
	    all_time_total_cost: number;
	    today_tokens: number;
	    all_time_total_tokens: number;

	    static createFrom(source: any = {}) {
	        return new UsageSummary(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total_requests = source["total_requests"];
	        this.all_time_total_requests = source["all_time_total_requests"];
	        this.today_requests = source["today_requests"];
	        this.success_requests = source["success_requests"];
	        this.failed_requests = source["failed_requests"];
	        this.total_input_tokens = source["total_input_tokens"];
	        this.total_output_tokens = source["total_output_tokens"];
	        this.total_cost = source["total_cost"];
	        this.today_cost = source["today_cost"];
	        this.all_time_total_cost = source["all_time_total_cost"];
	        this.today_tokens = source["today_tokens"];
	        this.all_time_total_tokens = source["all_time_total_tokens"];
	    }
	}

}
export namespace migration {

	export class Status {
	    state: string;
	    migration_id: string;
	    phase: string;
	    error: string;
	    database_path: string;
	    config_path: string;
	    backup_dir: string;
	    database_integrity: string;
	    backup_integrity: string;
	    retry_allowed: boolean;
	    database_existed: boolean;
	    source_mode: string;
	    endpoint_count_before: number;
	    endpoint_count_after: number;
	    split_endpoint_count: number;
	    derived_record_count: number;
	    request_log_count: number;
	    backup_manifest_sha256: string;

	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.migration_id = source["migration_id"];
	        this.phase = source["phase"];
	        this.error = source["error"];
	        this.database_path = source["database_path"];
	        this.config_path = source["config_path"];
	        this.backup_dir = source["backup_dir"];
	        this.database_integrity = source["database_integrity"];
	        this.backup_integrity = source["backup_integrity"];
	        this.retry_allowed = source["retry_allowed"];
	        this.database_existed = source["database_existed"];
	        this.source_mode = source["source_mode"];
	        this.endpoint_count_before = source["endpoint_count_before"];
	        this.endpoint_count_after = source["endpoint_count_after"];
	        this.split_endpoint_count = source["split_endpoint_count"];
	        this.derived_record_count = source["derived_record_count"];
	        this.request_log_count = source["request_log_count"];
	        this.backup_manifest_sha256 = source["backup_manifest_sha256"];
	    }
	}

}
