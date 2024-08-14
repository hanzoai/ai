import { MigrationInterface, QueryRunner } from 'typeorm'

export class AddApiKey1723572854165 implements MigrationInterface {
    public async up(queryRunner: QueryRunner): Promise<void> {
        await queryRunner.query(
            `CREATE TABLE IF NOT EXISTS "apikey" ("id" varchar PRIMARY KEY NOT NULL, 
                "apiKey" varchar NOT NULL, 
                "projectId" varchar NOT NULL,
                "apiSecret" varchar NOT NULL, 
                "keyName" varchar NOT NULL, 
                "updatedDate" datetime NOT NULL DEFAULT (datetime('now')));`
        )
    }

    public async down(queryRunner: QueryRunner): Promise<void> {
        await queryRunner.query(`DROP TABLE IF EXISTS "apikey";`)
    }
}
