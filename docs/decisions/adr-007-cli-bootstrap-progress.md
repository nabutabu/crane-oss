# ADR-007: CLI-owned layer-zero bootstrap progress

The first-zone, layer-zero bootstrap is an explicit operator command. Its
ordered phases are reported by `crane-bootstrap`; the command is safe to rerun
because schema installation and topology seeding are idempotent.

The database retains the canonical topology and the existing operation tables
for future API-driven subsequent-zone bootstrap. The layer-zero CLI does not
write phase rows or depend on them for recovery.
