-- github_comment_id: el ID del comentario que este hallazgo produjo en el
-- VCS (GitHub). Permite permalinkear desde el dashboard al comentario real
-- (donde el bloque ```suggestion``` se aplica con un clic) sin volver a
-- emparejar findings con comments_sent por huella — el ancla puede ser un
-- símbolo, no una línea (guía §3.3, §3.6.2).
--
-- NULL = el hallazgo no se publicó inline (perfil chill, filtro de
-- publicación, o corrida sin comentario) o el ID aún no se escribió.
ALTER TABLE findings ADD COLUMN github_comment_id TEXT NULL;

COMMENT ON COLUMN findings.github_comment_id IS 'ID del comentario publicado en el VCS para este hallazgo (perm-link del dashboard)';
