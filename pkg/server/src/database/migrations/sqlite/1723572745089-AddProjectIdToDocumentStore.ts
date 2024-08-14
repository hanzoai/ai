import { MigrationInterface, QueryRunner } from 'typeorm'

export class AddProjectIdToDocumentStore1723572745089 implements MigrationInterface {
    public async up(queryRunner: QueryRunner): Promise<void> {
        const columnExists = await queryRunner.hasColumn('document_store', 'projectId')
        if (!columnExists) await queryRunner.query(`ALTER TABLE "document_store" ADD COLUMN "projectId" TEXT;`)
    }

    public async down(queryRunner: QueryRunner): Promise<void> {
        await queryRunner.query(`ALTER TABLE "document_store" DROP COLUMN "projectId";`)
    }
}
