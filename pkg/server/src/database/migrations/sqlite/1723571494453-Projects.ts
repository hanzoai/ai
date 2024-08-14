import { MigrationInterface, QueryRunner } from 'typeorm'

export class Projects1723571494453 implements MigrationInterface {
    public async up(queryRunner: QueryRunner): Promise<void> {
        await queryRunner.query(
            `CREATE TABLE IF NOT EXISTS "projects" ("id" varchar PRIMARY KEY NOT NULL, "name" text NOT NULL, "projectId" text NOT NULL, "orgId" text NOT NULL, "createdDate" datetime NOT NULL DEFAULT (datetime('now')), "updatedDate" datetime NOT NULL DEFAULT (datetime('now')));`
        )
    }

    public async down(queryRunner: QueryRunner): Promise<void> {
        await queryRunner.query(`DROP TABLE projects`)
    }
}
