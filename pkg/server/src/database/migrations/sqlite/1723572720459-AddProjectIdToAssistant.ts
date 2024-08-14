import { MigrationInterface, QueryRunner } from 'typeorm'

export class AddProjectIdToAssistant1723572720459 implements MigrationInterface {
    public async up(queryRunner: QueryRunner): Promise<void> {
        const columnExists = await queryRunner.hasColumn('assistant', 'projectId')
        if (!columnExists) await queryRunner.query(`ALTER TABLE "assistant" ADD COLUMN "projectId" TEXT;`)
    }

    public async down(queryRunner: QueryRunner): Promise<void> {
        await queryRunner.query(`ALTER TABLE "assistant" DROP COLUMN "projectId";`)
    }
}
