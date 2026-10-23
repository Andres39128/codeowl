-- F6 (§3.3/§6): triage extendido — effort_score e impact_score 0-100 junto
-- a risk_score. Nullable: impact queda NULL sin índice del repo (degrada
-- visible en el dashboard); effort siempre es computable del diff. Se pisan
-- en cada corrida, mismo régimen best-effort que risk_score.
ALTER TABLE pull_requests
    ADD COLUMN effort_score INT NULL,
    ADD COLUMN impact_score INT NULL;
