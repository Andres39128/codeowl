-- Índice simbólico RAG de F4 (guía §3.3): una fila por símbolo indexado
-- (función, método, tipo) de los archivos del repo, con su embedding pgvector
-- para retrieval por similitud coseno. La extensión vector se crea acá: es la
-- primera vez que el schema la necesita.
--
-- Dims FIJAS en DDL (§3.3): vector(1536) — TODO insert debe ser 1536-dim. El
-- guard de settings de F4 (probe de dims contra el catálogo al habilitar un
-- proveedor rol embedding) y los checks de dims del gateway protegen el
-- invariante; cambiar de dims exige migración nueva.
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE repo_index (
    id              BIGINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repository_id   BIGINT       NOT NULL REFERENCES repositories (id),
    file            TEXT         NOT NULL,
    symbol          TEXT         NOT NULL,
    kind            TEXT         NOT NULL,
    start_line      INT          NOT NULL,
    end_line        INT          NOT NULL,
    embedding       vector(1536) NOT NULL,
    embedding_model TEXT         NOT NULL,
    -- Espejo de las dims de la columna embedding: el catálogo de PG
    -- (pg_attribute.typmod) es la fuente de verdad (§3.3), esta columna
    -- documenta qué modelo/dims generó la fila.
    dims            INT          NOT NULL,
    -- Imports file-level del archivo (deduped): insumo del grafo de imports
    -- para la expansión de contexto (§3.3).
    imports         TEXT[]       NOT NULL DEFAULT '{}',
    -- Hash del contenido del archivo al indexar: la reindexación se salta el
    -- archivo con hash igual y mismo embedding_model (retoma, §9.6).
    file_hash       TEXT         NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Índices de acceso (§3.3): lecturas por repo+archivo (expansión de imports,
-- re-index por archivo) y búsqueda coseno aproximada con HNSW (§3.3) — el
-- operador <=> con vector_cosine_ops es el que usa el retrieval del Reviewer.
CREATE INDEX repo_index_repository_id_file_idx ON repo_index (repository_id, file);
CREATE INDEX repo_index_embedding_hnsw_idx ON repo_index USING hnsw (embedding vector_cosine_ops);
