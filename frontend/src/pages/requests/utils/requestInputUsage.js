// Codex 的输入已包含缓存，Claude 的输入只计普通输入。旧记录按 Responses 路径识别。
export const requestInputIncludesCache = (requestFamily, requestPath) => {
  const family = String(requestFamily || '').trim().toLowerCase();
  const path = String(requestPath || '').trim().toLowerCase();
  return family === 'codex' || (family !== 'claude' && /\/responses(?:\/compact)?$/.test(path));
};

export const fullInputTokens = (includesCache, input, created, read) => {
  if (!Number.isFinite(input) || input < 0) return null;
  if (includesCache) return input;
  if (!Number.isFinite(created) || created < 0 || !Number.isFinite(read) || read < 0) return null;
  return input + created + read;
};

export const normalizeInputUsage = (request) => {
  const input = request.input_tokens ?? request.inputTokens ?? null;
  const read = request.cache_read_tokens ?? request.cacheReadTokens ?? null;
  const created5m = request.cache_creation_5m_tokens ?? request.cacheCreation5mTokens ?? null;
  const created1h = request.cache_creation_1h_tokens ?? request.cacheCreation1hTokens ?? null;
  const created = request.cache_creation_tokens ?? request.cacheCreationTokens
    ?? (Number.isFinite(created5m) && Number.isFinite(created1h) ? created5m + created1h : null);
  const includesCache = requestInputIncludesCache(request.request_family ?? request.requestFamily, request.path);
  const total = fullInputTokens(includesCache, input, created, read);
  return {
    inputTokens: input, cacheReadTokens: read, cacheCreationTokens: created,
    cacheCreation5mTokens: created5m, cacheCreation1hTokens: created1h,
    cacheRatio: total > 0 && Number.isFinite(read) && read >= 0 && read <= total ? read / total : null
  };
};
