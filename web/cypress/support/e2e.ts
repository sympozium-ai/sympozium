// Support file — loaded before every spec.

// ── Auth: inject API token via visit callback ───────────────────────────────
// Overrides cy.visit to inject the token into localStorage before the app
// reads it. Token is read from CYPRESS_API_TOKEN env var.
Cypress.Commands.overwrite(
  "visit",
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (originalFn: any, url: string | Partial<Cypress.VisitOptions>, options?: Partial<Cypress.VisitOptions>) => {
    const token = Cypress.env("API_TOKEN");
    if (!token) return originalFn(url, options);

    const opts: Partial<Cypress.VisitOptions> = { ...options };
    const originalOnBeforeLoad = opts.onBeforeLoad;
    opts.onBeforeLoad = (win: Cypress.AUTWindow) => {
      win.localStorage.setItem("sympozium_token", token);
      win.localStorage.setItem("sympozium_namespace", "default");
      if (originalOnBeforeLoad) originalOnBeforeLoad(win);
    };
    return originalFn(url, opts);
  },
);

// ── Custom commands ─────────────────────────────────────────────────────────
declare global {
  namespace Cypress {
    interface Chainable {
      /** Click the Next button in the onboarding wizard. */
      wizardNext(): Chainable<void>;
      /** Click the Back button in the onboarding wizard. */
      wizardBack(): Chainable<void>;
      /** Delete an instance by name via API (cleanup helper). */
      deleteInstance(name: string): Chainable<void>;
      /** Create a minimal LM Studio SympoziumInstance via API. */
      createLMStudioInstance(
        name: string,
        opts?: { skills?: string[] },
      ): Chainable<void>;
      /** Create a minimal llama-server SympoziumInstance via API. */
      createLlamaServerInstance(
        name: string,
        opts?: { skills?: string[] },
      ): Chainable<void>;
      /** Dispatch an ad-hoc run against an instance via API. Returns the created run name. */
      dispatchRun(
        instanceRef: string,
        task: string,
        opts?: { name?: string },
      ): Chainable<string>;
      /** Poll status.phase of an AgentRun until it reaches a terminal phase. */
      waitForRunTerminal(
        runName: string,
        timeoutMs?: number,
      ): Chainable<string>;
      /** Poll an API URL until it returns 404 (resource fully deleted). */
      waitForDeleted(path: string, timeoutMs?: number): Chainable<void>;
      /** Delete an AgentRun by name (cleanup helper). */
      deleteRun(name: string): Chainable<void>;
      /** Delete a Ensemble by name (cleanup helper). */
      deleteEnsemble(name: string): Chainable<void>;
      /** Delete a SympoziumSchedule by name (cleanup helper). */
      deleteSchedule(name: string): Chainable<void>;
      /** Delete an MCPServer by name (cleanup helper). */
      deleteMcpServer(name: string): Chainable<void>;
      /** Delete a Model by name (cleanup helper). */
      deleteModel(name: string): Chainable<void>;
    }
  }
}

function authHeaders(): Record<string, string> {
  const token = Cypress.env("API_TOKEN");
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (token) h["Authorization"] = `Bearer ${token}`;
  return h;
}

Cypress.Commands.add("wizardNext", () => {
  cy.contains("button", "Next")
    .should("not.be.disabled")
    .click({ force: true });
});

Cypress.Commands.add("wizardBack", () => {
  cy.contains("button", "Back").click({ force: true });
});

Cypress.Commands.add("deleteInstance", (name: string) => {
  cy.request({
    method: "DELETE",
    url: `/api/v1/instances/${name}?namespace=default`,
    headers: authHeaders(),
    failOnStatusCode: false,
  });
});

Cypress.Commands.add("deleteRun", (name: string) => {
  cy.request({
    method: "DELETE",
    url: `/api/v1/runs/${name}?namespace=default`,
    headers: authHeaders(),
    failOnStatusCode: false,
  });
});

Cypress.Commands.add("deleteEnsemble", (name: string) => {
  cy.request({
    method: "DELETE",
    url: `/api/v1/ensembles/${name}?namespace=default`,
    headers: authHeaders(),
    failOnStatusCode: false,
  });
});

Cypress.Commands.add("deleteSchedule", (name: string) => {
  cy.request({
    method: "DELETE",
    url: `/api/v1/schedules/${name}?namespace=default`,
    headers: authHeaders(),
    failOnStatusCode: false,
  });
});

Cypress.Commands.add("deleteMcpServer", (name: string) => {
  cy.request({
    method: "DELETE",
    url: `/api/v1/mcpservers/${name}?namespace=default`,
    headers: authHeaders(),
    failOnStatusCode: false,
  });
});

Cypress.Commands.add("deleteModel", (name: string) => {
  cy.request({
    method: "DELETE",
    url: `/api/v1/models/${name}`,
    headers: authHeaders(),
    failOnStatusCode: false,
  });
});

Cypress.Commands.add("createLMStudioInstance", (name: string, opts) => {
  const body: Record<string, unknown> = {
    name,
    provider: "lm-studio",
    model: "qwen/qwen3.5-9b",
    baseURL: "http://host.docker.internal:1234/v1",
  };
  if (opts?.skills?.length) {
    body.skills = opts.skills.map((s) => ({ skillPackRef: s }));
  }
  cy.request({
    method: "POST",
    url: "/api/v1/instances?namespace=default",
    headers: authHeaders(),
    body,
    failOnStatusCode: false,
  }).then((resp) => {
    if (resp.status >= 400 && resp.status !== 409) {
      throw new Error(
        `createLMStudioInstance failed (${resp.status}): ${JSON.stringify(resp.body)}`,
      );
    }
  });
});

Cypress.Commands.add("createLlamaServerInstance", (name: string, opts) => {
  const body: Record<string, unknown> = {
    name,
    provider: "llama-server",
    model: "default",
    baseURL: "http://host.docker.internal:8080/v1",
  };
  if (opts?.skills?.length) {
    body.skills = opts.skills.map((s) => ({ skillPackRef: s }));
  }
  cy.request({
    method: "POST",
    url: "/api/v1/instances?namespace=default",
    headers: authHeaders(),
    body,
    failOnStatusCode: false,
  }).then((resp) => {
    if (resp.status >= 400 && resp.status !== 409) {
      throw new Error(
        `createLlamaServerInstance failed (${resp.status}): ${JSON.stringify(resp.body)}`,
      );
    }
  });
});

Cypress.Commands.add(
  "dispatchRun",
  (instanceRef: string, task: string, opts) => {
    return cy
      .request({
        method: "POST",
        url: "/api/v1/runs?namespace=default",
        headers: authHeaders(),
        body: {
          instanceRef,
          task,
          ...(opts?.name ? { name: opts.name } : {}),
        },
      })
      .then((resp) => {
        expect(resp.status).to.be.oneOf([200, 201]);
        const name = resp.body?.metadata?.name as string;
        expect(name).to.be.a("string").and.not.be.empty;
        return cy.wrap(name);
      });
  },
);

Cypress.Commands.add("waitForDeleted", (path: string, timeoutMs = 30000) => {
  const started = Date.now();
  const poll = (): Cypress.Chainable<void> => {
    return (cy
      .request({
        url: path,
        headers: authHeaders(),
        failOnStatusCode: false,
      })
      .then((resp): void => {
        if (resp.status === 404) {
          return;
        }
        if (Date.now() - started > timeoutMs) {
          throw new Error(
            `waitForDeleted(${path}) timed out; last status=${resp.status}`,
          );
        }
        cy.wait(1000, { log: false });
        poll();
      }) as unknown as Cypress.Chainable<void>);
  };
  return poll();
});

Cypress.Commands.add(
  "waitForRunTerminal",
  (runName: string, timeoutMs = 180000) => {
    const started = Date.now();
    const poll = (): Cypress.Chainable<string> => {
      return cy
        .request({
          url: `/api/v1/runs/${runName}?namespace=default`,
          headers: authHeaders(),
          failOnStatusCode: false,
        })
        .then((resp) => {
          const phase = resp.body?.status?.phase as string | undefined;
          if (phase === "Succeeded" || phase === "Failed") {
            return cy.wrap(phase);
          }
          if (Date.now() - started > timeoutMs) {
            throw new Error(
              `waitForRunTerminal(${runName}) timed out; last phase=${phase ?? "none"}`,
            );
          }
          cy.wait(2000, { log: false });
          return poll();
        });
    };
    return poll();
  },
);

export {};
