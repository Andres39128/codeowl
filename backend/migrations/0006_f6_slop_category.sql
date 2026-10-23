-- F6 (§3.3/§6): `slop` entra al conjunto cerrado de findings.category — la
-- publica el SlopDetector (rol cheap) sobre código de relleno generado por
-- IA. Forward-only: se reemplaza el CHECK anónimo de 0003 por la versión
-- ampliada; el histórico no cambia (ninguna fila previa usa la categoría).
ALTER TABLE findings DROP CONSTRAINT findings_category_check;
ALTER TABLE findings
    ADD CHECK (category IN ('security', 'logic', 'performance', 'style', 'tests', 'slop', 'other'));
