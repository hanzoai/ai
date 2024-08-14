import { MigrationInterface, QueryRunner } from 'typeorm'

export class AddProjectIdToTool1723572753767 implements MigrationInterface {
    public async up(queryRunner: QueryRunner): Promise<void> {
        const columnExists = await queryRunner.hasColumn('tool', 'projectId')
        if (!columnExists) await queryRunner.query(`ALTER TABLE "tool" ADD COLUMN "projectId" TEXT;`)
    }

    public async down(queryRunner: QueryRunner): Promise<void> {
        await queryRunner.query(`ALTER TABLE "tool" DROP COLUMN "projectId";`)
    }
}
