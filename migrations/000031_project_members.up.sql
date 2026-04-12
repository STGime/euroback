-- Team collaboration: project members with role-based access control.
--
-- Roles (ordered by permission level):
--   viewer    — read-only access to data, logs, compliance
--   developer — viewer + schema DDL, data writes, function management
--   admin     — developer + settings, API keys, vault, member invites
--   owner     — admin + project deletion, member role changes, ownership transfer
--
-- Every existing project gets a backfilled 'owner' row so the new
-- membership-based access checks don't break existing single-owner projects.

BEGIN;

CREATE TABLE public.project_members (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    role        TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'developer', 'viewer')),
    invited_by  UUID REFERENCES public.platform_users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, user_id)
);

CREATE INDEX idx_project_members_project ON public.project_members(project_id);
CREATE INDEX idx_project_members_user ON public.project_members(user_id);

CREATE TABLE public.project_invitations (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id  UUID NOT NULL REFERENCES public.projects(id) ON DELETE CASCADE,
    email       TEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('admin', 'developer', 'viewer')),
    token_hash  TEXT NOT NULL,
    invited_by  UUID NOT NULL REFERENCES public.platform_users(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, email)
);

CREATE INDEX idx_project_invitations_project ON public.project_invitations(project_id);
CREATE INDEX idx_project_invitations_token ON public.project_invitations(token_hash);

-- Backfill: every existing active project gets an 'owner' membership row
-- for its current owner_id. This makes the transition seamless — existing
-- projects continue to work with the new membership-based access checks.
INSERT INTO public.project_members (project_id, user_id, role)
SELECT id, owner_id, 'owner'
FROM public.projects
WHERE owner_id IS NOT NULL
ON CONFLICT (project_id, user_id) DO NOTHING;

COMMIT;
