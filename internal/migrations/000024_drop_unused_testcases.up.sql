DO $$
BEGIN
    -- Prevent writes between the empty-table check and the drop.
    LOCK TABLE testcases IN ACCESS EXCLUSIVE MODE;

    IF EXISTS (SELECT 1 FROM testcases) THEN
        RAISE EXCEPTION 'Refusing to drop non-empty testcases table';
    END IF;

    DROP TABLE testcases;
END;
$$;
