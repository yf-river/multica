export function attachBrowserAuditEvents(page, { isAuditedRequest, requestPath = browserRequestPath } = {}) {
  if (typeof isAuditedRequest !== "function") {
    throw new TypeError("attachBrowserAuditEvents requires isAuditedRequest");
  }

  const requests = [];
  const failedRequests = [];
  const errors = attachBrowserErrorEvents(page);

  const onRequest = (request) => {
    if (!isAuditedRequest(request.url())) return;
    requests.push({
      url: request.url(),
      method: request.method(),
      type: request.resourceType(),
      start: Date.now(),
    });
  };
  const onResponse = (response) => {
    if (!isAuditedRequest(response.url())) return;
    const request = response.request();
    const item = [...requests].reverse().find((candidate) => candidate.url === response.url() && candidate.method === request.method() && !candidate.status);
    if (!item) return;
    item.status = response.status();
    item.ms = Date.now() - item.start;
  };
  const onRequestFailed = (request) => {
    const failure = request.failure()?.errorText || "unknown";
    if (isAuditedRequest(request.url()) && failure !== "net::ERR_ABORTED") {
      failedRequests.push({ path: requestPath(request.url()), method: request.method(), failure });
    }
  };

  page.on("request", onRequest);
  page.on("response", onResponse);
  page.on("requestfailed", onRequestFailed);

  return {
    requests,
    failedRequests,
    consoleErrors: errors.consoleErrors,
    pageErrors: errors.pageErrors,
    detach() {
      page.off("request", onRequest);
      page.off("response", onResponse);
      page.off("requestfailed", onRequestFailed);
      errors.detach();
    },
  };
}

export function attachBrowserErrorEvents(page) {
  const consoleErrors = [];
  const pageErrors = [];
  const onConsole = (message) => {
    if (message.type() === "error" && !message.text().startsWith("Failed to load resource:")) {
      consoleErrors.push(message.text().slice(0, 500));
    }
  };
  const onPageError = (error) => {
    pageErrors.push(error.message.slice(0, 500));
  };

  page.on("console", onConsole);
  page.on("pageerror", onPageError);

  return {
    consoleErrors,
    pageErrors,
    detach() {
      page.off("console", onConsole);
      page.off("pageerror", onPageError);
    },
  };
}

export function browserRequestPath(url) {
  try {
    const parsed = new URL(url);
    return `${parsed.pathname}${parsed.search}`;
  } catch {
    return url;
  }
}
