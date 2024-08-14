import { MigrationInterface, QueryRunner } from 'typeorm'

export class AddProjectIdToVariable1723572763202 implements MigrationInterface {
    public async up(queryRunner: QueryRunner): Promise<void> {
        const columnExists = await queryRunner.hasColumn('variable', 'projectId')
        if (!columnExists) await queryRunner.query(`ALTER TABLE "variable" ADD COLUMN "projectId" TEXT;`)
    }

    public async down(queryRunner: QueryRunner): Promise<void> {
        await queryRunner.query(`ALTER TABLE "variable" DROP COLUMN "projectId";`)
    }
}
