// Public docs: server-rendered and pre-rendered at build time so search
// engines and AI answer engines can read every chapter without JS or a
// session. Lives outside the (app) group on purpose: that group is
// ssr=false and redirects anonymous visitors to /login.
export const ssr = true;
export const prerender = true;
