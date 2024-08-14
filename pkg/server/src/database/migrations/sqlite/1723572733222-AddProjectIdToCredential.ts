import { MigrationInterface, QueryRunner } from 'typeorm'

export class AddProjectIdToCredential1723572733222 implements MigrationInterface {
    public async up(queryRunner: QueryRunner): Promise<void> {
        const columnExists = await queryRunner.hasColumn('credential', 'projectId')
        if (!columnExists) await queryRunner.query(`ALTER TABLE "credential" ADD COLUMN "projectId" TEXT;`)
    }

    public async down(queryRunner: QueryRunner): Promise<void> {
        await queryRunner.query(`ALTER TABLE "credential" DROP COLUMN "projectId";`)
    }
}
