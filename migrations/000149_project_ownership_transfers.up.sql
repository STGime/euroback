-- Project ownership transfer (#846).
--
-- Lets a project owner hand the whole project to another person's account:
-- the recipient accepts via an emailed, expiring token (consent), and on
-- accept the project's owner_id is reassigned, the old owner is demoted to
-- admin, and (for a paid project) the old owner's subscription is cancelled
-- at period end so the new owner's billing takes over at the next cycle
-- without ever double-charging the period the old owner already paid.
--
-- Mirrors public.project_invitations (000031): only the SHA-256 token_hash
-- is stored, never the raw token (which lives only in the email URL). State
-- is implicit — pending = accepted_at IS NULL AND expires_at > now().
-- UNIQUE (project_id) allows at most one pending/completed transfer row per
-- project; a re-initiate upserts it.
CREATE TABLE public.project_ownership_transfers (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    new_owner_email TEXT NOT NULL,
    token_hash      TEXT NOT NULL,
    initiated_by    UUID NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    expires_at      TIMESTAMPTZ NOT NULL,
    accepted_at     TIMESTAMPTZ,
    sent_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id)
);

CREATE INDEX idx_project_ownership_transfers_project ON public.project_ownership_transfers(project_id);
CREATE INDEX idx_project_ownership_transfers_token ON public.project_ownership_transfers(token_hash);
