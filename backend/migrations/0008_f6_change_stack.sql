-- F6 (§3.3/§6): change stack — cohortes de dependencia del diff (orden
-- topológico por imports del repo revisado, tests al final por heurística
-- de ruta). JSONB nullable: NULL sin índice del repo (degrada visible en
-- el dashboard); se pisa en cada corrida, última corrida gana (mismo
-- régimen que risk_score).
ALTER TABLE pull_requests ADD COLUMN change_stack JSONB NULL;
