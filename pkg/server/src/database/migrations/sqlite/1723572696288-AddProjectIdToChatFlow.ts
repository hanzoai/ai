import { MigrationInterface, QueryRunner } from 'typeorm'

export class AddProjectIdToChatFlow1723572696288 implements MigrationInterface {
    public async up(queryRunner: QueryRunner): Promise<void> {
        const columnExists = await queryRunner.hasColumn('chat_flow', 'projectId')
        if (!columnExists) await queryRunner.query(`ALTER TABLE "chat_flow" ADD COLUMN "projectId" TEXT;`)
    }

    public async down(queryRunner: QueryRunner): Promise<void> {
        await queryRunner.query(`ALTER TABLE "chat_flow" DROP COLUMN "projectId";`)
    }
}
