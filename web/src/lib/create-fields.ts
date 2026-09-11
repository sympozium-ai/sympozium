import type { SkillRef } from "./api";

/**
 * The wizard collects inline per-skill configuration (web endpoint limits,
 * GitHub repo) that differs from the SkillPack selection itself. Every creation
 * path (Agent and Ensemble, in all three ensemble call sites) must turn those
 * into SkillPack params the same way — previously each call site mapped a
 * different subset and silently dropped the rest.
 */
export interface WizardSkillConfig {
  skills: string[];
  webEndpointRPM?: string;
  webEndpointHostname?: string;
  githubRepo?: string;
}

/** SkillPack params, keyed by SkillPack name, from the wizard's inline config. */
export function skillParamsFromWizard(
  r: WizardSkillConfig,
): Record<string, Record<string, string>> {
  const out: Record<string, Record<string, string>> = {};
  if (r.skills.includes("web-endpoint")) {
    const params: Record<string, string> = {};
    if (r.webEndpointRPM && r.webEndpointRPM !== "60") {
      params.rate_limit_rpm = r.webEndpointRPM;
    }
    if (r.webEndpointHostname) params.hostname = r.webEndpointHostname;
    if (Object.keys(params).length > 0) out["web-endpoint"] = params;
  }
  if (r.skills.includes("github-gitops") && r.githubRepo) {
    out["github-gitops"] = { repo: r.githubRepo };
  }
  return out;
}

/** Agent create API shape: one SkillRef per selected pack, with params attached. */
export function skillRefsFromWizard(r: WizardSkillConfig): SkillRef[] {
  const params = skillParamsFromWizard(r);
  return r.skills.map((skillPackRef) =>
    params[skillPackRef] ? { skillPackRef, params: params[skillPackRef] } : { skillPackRef },
  );
}
