-- For sqlc only, which reads this before the migrations: TrueUp creates its
-- schema itself before it migrates (createSchema in migrate.go), since goose
-- keeps its version table there and needs it before the first migration
-- runs. sqlc needs it to accept the first migration's COMMENT ON SCHEMA.
-- Nothing applies this file to a database.
CREATE SCHEMA netbox_trueup_ai;
