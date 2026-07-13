import type { Logger } from "../logger";
import { noopLogger } from "../logger";
import { getCurrentSlug } from "../platform/workspace-storage";
import { createRequestId } from "../utils";
import { ApiResponseValidationError } from "./schema";

/** Identifies the calling client through the X-Client-* request headers. */
export interface ApiClientIdentity {
  /** Logical client kind. The server recognizes web, desktop, CLI and daemon clients. */
  platform?: string;
  /** Client release, tag or commit identifier. */
  version?: string;
  /** Operating system reported by native clients. */
  os?: string;
}

export interface ApiClientOptions {
  logger?: Logger;
  onUnauthorized?: () => void;
  identity?: ApiClientIdentity;
}

export type JsonRequestInit = RequestInit & {
  responseMayHaveCommitted?: boolean;
  extraHeaders?: Record<string, string>;
};

export class ApiError extends Error {
  readonly status: number;
  readonly statusText: string;
  readonly body?: unknown;

  constructor(message: string, status: number, statusText: string, body?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.statusText = statusText;
    this.body = body;
  }
}

export class ApiTransportError extends Error {
  readonly endpoint: string;
  readonly mayHaveCommitted: boolean;
  readonly cause: unknown;

  constructor(endpoint: string, mayHaveCommitted: boolean, cause: unknown) {
    super(`API transport failed: ${endpoint}`);
    this.name = "ApiTransportError";
    this.endpoint = endpoint;
    this.mayHaveCommitted = mayHaveCommitted;
    this.cause = cause;
  }
}

function isMutationOutcomeUnknown(error: unknown): boolean {
  return (
    (error instanceof ApiTransportError || error instanceof ApiResponseValidationError) &&
    error.mayHaveCommitted
  );
}

export async function executeRecoverableMutation<Result>(
  mutate: () => Promise<Result>,
  clearPending: () => void,
): Promise<Result> {
  try {
    const result = await mutate();
    clearPending();
    return result;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) clearPending();
    throw error;
  }
}

export class ApiTransport {
  protected readonly baseUrl: string;
  private token: string | null = null;
  protected readonly logger: Logger;
  private readonly options: ApiClientOptions;

  constructor(baseUrl: string, options?: ApiClientOptions) {
    this.baseUrl = baseUrl;
    this.options = options ?? {};
    this.logger = options?.logger ?? noopLogger;
  }

  getBaseUrl(): string {
    return this.baseUrl;
  }

  setToken(token: string | null): void {
    this.token = token;
  }

  private readCsrfToken(): string | null {
    if (typeof document === "undefined") return null;
    const match = document.cookie.split("; ").find((cookie) => cookie.startsWith("multica_csrf="));
    return match ? match.split("=")[1] ?? null : null;
  }

  protected authHeaders(): Record<string, string> {
    const headers: Record<string, string> = {};
    if (this.token) headers.Authorization = `Bearer ${this.token}`;
    const slug = getCurrentSlug();
    if (slug) headers["X-Workspace-Slug"] = slug;
    const csrf = this.readCsrfToken();
    if (csrf) headers["X-CSRF-Token"] = csrf;
    const identity = this.options.identity;
    if (identity?.platform) headers["X-Client-Platform"] = identity.platform;
    if (identity?.version) headers["X-Client-Version"] = identity.version;
    if (identity?.os) headers["X-Client-OS"] = identity.os;
    return headers;
  }

  protected handleUnauthorized(): void {
    this.token = null;
    this.options.onUnauthorized?.();
  }

  protected async parseErrorMessage(res: Response, fallback: string): Promise<string> {
    try {
      const data = (await res.json()) as { error?: string };
      return typeof data.error === "string" && data.error ? data.error : fallback;
    } catch {
      return fallback;
    }
  }

  private async parseErrorBody(res: Response, fallback: string): Promise<{ message: string; body: unknown }> {
    try {
      const data = (await res.json()) as { error?: string };
      const message = typeof data.error === "string" && data.error ? data.error : fallback;
      return { message, body: data };
    } catch {
      return { message: fallback, body: undefined };
    }
  }

  protected async fetchRaw(path: string, init?: RequestInit & { extraHeaders?: Record<string, string> }): Promise<Response> {
    const requestId = createRequestId();
    const start = Date.now();
    const method = init?.method ?? "GET";
    const headers: Record<string, string> = {
      "X-Request-ID": requestId,
      ...this.authHeaders(),
      ...(init?.extraHeaders ?? {}),
      ...((init?.headers as Record<string, string>) ?? {}),
    };
    this.logger.info(`→ ${method} ${path}`, { rid: requestId });

    let response: Response;
    try {
      response = await fetch(`${this.baseUrl}${path}`, { ...init, headers, credentials: "include" });
    } catch (error) {
      const mayHaveCommitted = !["GET", "HEAD", "OPTIONS"].includes(method.toUpperCase());
      this.logger.warn(`← transport error ${path}`, {
        rid: requestId,
        duration: `${Date.now() - start}ms`,
        mayHaveCommitted,
      });
      throw new ApiTransportError(`${method.toUpperCase()} ${path}`, mayHaveCommitted, error);
    }

    if (!response.ok) {
      if (response.status === 401) this.handleUnauthorized();
      const { message, body } = await this.parseErrorBody(
        response,
        `API error: ${response.status} ${response.statusText}`,
      );
      const logLevel = response.status >= 500 ? "error" : "warn";
      this.logger[logLevel](`← ${response.status} ${path}`, {
        rid: requestId,
        duration: `${Date.now() - start}ms`,
        error: message,
      });
      throw new ApiError(message, response.status, response.statusText, body);
    }

    this.logger.info(`← ${response.status} ${path}`, {
      rid: requestId,
      duration: `${Date.now() - start}ms`,
    });
    return response;
  }

  protected async parseSuccessJson<T>(response: Response, endpoint: string, mayHaveCommitted: boolean): Promise<T> {
    try {
      return (await response.json()) as T;
    } catch {
      this.logger.warn("API response body is not valid JSON", { endpoint, status: response.status });
      throw new ApiResponseValidationError(endpoint, mayHaveCommitted);
    }
  }

  protected async retryUnknownMutationOnce<T>(attempt: () => Promise<T>): Promise<T> {
    try {
      return await attempt();
    } catch (error) {
      if (!isMutationOutcomeUnknown(error)) throw error;
      return attempt();
    }
  }

  protected async fetch<T>(path: string, init?: JsonRequestInit): Promise<T> {
    const { responseMayHaveCommitted, extraHeaders, ...requestInit } = init ?? {};
    const method = (requestInit.method ?? "GET").toUpperCase();
    const response = await this.fetchRaw(path, {
      ...requestInit,
      extraHeaders: { "Content-Type": "application/json", ...extraHeaders },
    });
    if (response.status === 204) return undefined as T;
    const mayHaveCommitted = responseMayHaveCommitted ?? !["GET", "HEAD", "OPTIONS"].includes(method);
    return this.parseSuccessJson<T>(response, `${method} ${path}`, mayHaveCommitted);
  }
}
