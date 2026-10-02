# Runbook de backups — codeowl

> Checklist F0 (guía §6): "`deploy/backup.md`: runbook de dump diario con
> restore verificado y respaldo de la master key (§9.2, §9.10)".
> Regla de §9.10: **un dump jamás restaurado no es backup** — la verificación
> mensual no es opcional. Sin la master key (§9.2), el dump es indecifrable
> para las columnas cifradas: la master key se respalda **por separado y
> fuera del dump**.

## Layout en el VPS (self-host, §3.5)

```
~/opt/codeowl/
├── env                     # config no secreta + DATABASE_URL (chmod 600, gestionado a mano)
├── secrets/                # credenciales montadas por las unidades (chmod 600, a mano)
│   ├── postgres_password   #   unidad postgres (POSTGRES_PASSWORD_FILE)
│   ├── master_key          #   unidades api y worker
│   ├── admin_password      #   unidad api (seed F0, §3.4)
│   ├── github_webhook_secret  # unidad api
│   └── github_app_private_key # unidades api y worker
├── bin/                    # binarios api y worker (just deploy, §9.13)
├── backend/migrations/
└── analyzer/               # fuente que reconstruye analyzer-build.service
```

Backups del dump: `~/backups/postgres/` (local) **y una copia fuera del VPS**
(rsync a otra máquina/objeto storage). Un dump solo en el mismo disco que la
BD no sobrevive al disco.

---

## 1. Dump diario (§9.10)

Dump en formato custom (`-Fc`, comprimido y restaurable selectivo) del único
servicio de datos: postgres (rootless, unidad `postgres.service`):

```bash
mkdir -p ~/backups/postgres
podman exec codeowl-postgres \
  pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc \
  > ~/backups/postgres/codeowl-$(date +%Y%m%d).dump
```

(`$POSTGRES_USER`/`$POSTGRES_DB` salen de `~/opt/codeowl/env` — cargalos con
`set -a; . ~/opt/codeowl/env; set +a` si el cron no hereda el entorno.)

### Programación

Cron del usuario del deploy (con linger activo, corre sin sesión abierta):
`crontab -e`

```cron
# dump diario 03:00 + copia fuera del VPS + retención 14 días
0 3 * * * $HOME/opt/codeowl/bin/backup-dump.sh
```

Con `~/opt/codeowl/bin/backup-dump.sh`:

```bash
#!/usr/bin/env bash
# dump diario de codeowl (guía §9.10) + rsync fuera del VPS + retención local
set -euo pipefail
set -a; . "$HOME/opt/codeowl/env"; set +a
dest="$HOME/backups/postgres"
mkdir -p "$dest"
podman exec codeowl-postgres pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc \
  > "$dest/codeowl-$(date +%Y%m%d).dump"
# BACKUP_DESTINO=usuario@otra-maquina:~/backups/codeowl — rsync a un medio distinto
rsync -a "$dest/codeowl-$(date +%Y%m%d).dump" "${BACKUP_DESTINO:?definí BACKUP_DESTINO en ~/opt/codeowl/env}"
find "$dest" -name 'codeowl-*.dump' -mtime +14 -delete
```

## 2. Restore verificado — mensual (§9.10)

Levantar una instancia **efímera** con la misma imagen, restaurar y validar;
al terminar, destruirla:

```bash
set -a; . ~/opt/codeowl/env; set +a
dump=$(ls -t ~/backups/postgres/codeowl-*.dump | head -1)

# 1) instancia efímera (sin puertos publicados, red propia)
podman run -d --name codeowl-restore-check \
  -e POSTGRES_USER="$POSTGRES_USER" -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
  -e POSTGRES_DB="$POSTGRES_DB" docker.io/pgvector/pgvector:pg18
sleep 5

# 2) restore del dump más reciente
podman cp "$dump" codeowl-restore-check:/tmp/restore.dump
podman exec codeowl-restore-check \
  pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner --exit-on-error /tmp/restore.dump

# 3) verificación: tablas esperadas + filas de tablas núcleo (§3.3)
podman exec codeowl-restore-check psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c '\dt'
podman exec codeowl-restore-check psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c 'SELECT count(*) FROM users;' -c 'SELECT count(*) FROM repositories;'

# 4) destrucción de la instancia efímera
podman rm -f codeowl-restore-check
```

Registrar la fecha y el resultado de cada drill (calendario del equipo o
issue mensual). Si el restore falla: el dump más reciente NO es backup —
corregir el procedimiento antes de seguir operando.

## 3. Respaldo de la master key (§9.2, §9.10)

La master key (`~/opt/codeowl/secrets/master_key`) **no viaja en el dump ni
en el mismo medio**: sin ella, `llm_providers`, `repositories` y toda columna
cifrada (AES-256-GCM) quedan irrecuperables.

1. **Cifrado offline**: `age` o `gpg` con passphrase fuerte, nunca plano:
   ```bash
   age -p -o master_key.age ~/opt/codeowl/secrets/master_key
   ```
2. **Dos medios distintos** del `.age` (p. ej. gestor de secretos del operador
   + un disco/pendrive guardado fuera del VPS). Nunca en el mismo directorio
   que los dumps.
3. **Drill de descifrado** junto con el restore mensual: recuperar el `.age`,
   descifrar y comparar contra el hash del archivo en el VPS:
   ```bash
   age -d -o /tmp/master_key.check master_key.age
   sha256sum /tmp/master_key.check ~/opt/codeowl/secrets/master_key
   rm -f /tmp/master_key.check
   ```
4. **Tras una rotación** (§9.2: dual `MASTER_KEY` + `MASTER_KEY_PREVIOUS`,
   `RotationJob` de re-cifrado): regenerar el backup con la key nueva y
   conservar la anterior cifrada hasta confirmar que el `RotationJob`
   terminó y `_PREVIOUS` se retiró del env.

## 4. Qué respalda cada cosa

| Elemento | Respaldo | Frecuencia |
|----------|----------|------------|
| Datos del sistema (todas las tablas, §3.3) | dump diario + rsync fuera del VPS | diaria |
| Restaurabilidad | restore verificado en instancia efímera | mensual |
| Master key | `.age` cifrado en dos medios + drill de descifrado | al cambiar + mensual |
| Código y prompts | git (el repo es el backup) | cada PR |

## Notas de deploy/ (inventario de unidades)

- `postgres.container` y `caddy.container`: fuentes Quadlet (generan
  `postgres.service` y `caddy.service` en `systemctl --user`); los nombres de
  unidad generados y el `analyzer-build.service` de `analyzer.build` fueron
  verificados contra el generador de podman 5.7.
- `api.service` y `worker.service`: unidades de usuario escritas a mano para
  los binarios de host (mapa servicios: el generador solo consume
  `.container`/`.build`).
- `analyzer.build`: la imagen se construye en el VPS en cada deploy (§9.13) —
  no existe imagen preconstruida que respaldar; la fuente vive en git.
