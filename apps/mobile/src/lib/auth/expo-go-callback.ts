/** Build a root callback from the URL that actually launched this Expo Go project. */
export function expoGoCallback(launchUrl: string | null): string | null {
  if (!launchUrl) return null;
  try {
    const url = new URL(launchUrl);
    if (!["exp:", "exps:"].includes(url.protocol) || !url.host) return null;
    // Keep the project location, but discard any route and auth/query data.
    const projectPath = url.pathname.split("/--/")[0] ?? "";
    url.pathname = `${projectPath.replace(/\/$/, "")}/--/`;
    url.search = "";
    url.hash = "";
    url.username = "";
    url.password = "";
    return url.toString();
  } catch {
    return null;
  }
}

/** Native Expo Go exposes the development host even when launch URLs are absent. */
export function expoGoCallbackFromHost(host: string | undefined): string | null {
  if (!host) return null;
  try {
    const url = new URL(host.includes("://") ? host : `http://${host}`);
    if (
      !["http:", "https:"].includes(url.protocol) ||
      !url.host || url.username || url.password ||
      url.pathname !== "/" || url.search || url.hash
    ) return null;
    const scheme = url.protocol === "https:" ? "exps" : "exp";
    return expoGoCallback(`${scheme}://${url.host}`);
  } catch {
    return null;
  }
}
