import { StatusCodes } from 'http-status-codes'
import { getRunningExpressApp } from '../../utils/getRunningExpressApp'
import { Projects } from '../../database/entities/Projects'
import { InternalHanzoError } from '../../errors/internalHanzoError'
import { getErrorMessage } from '../../errors/utils'

const createProject = async (newProject: Projects) => {
    try {
        const appServer = getRunningExpressApp()
        const searchProject = await appServer.AppDataSource.getRepository(Projects).findOneBy({ projectId: newProject.projectId })
        if (!searchProject) {
            const project = await appServer.AppDataSource.getRepository(Projects).create(newProject)
            const dbResponse = await appServer.AppDataSource.getRepository(Projects).save(project)
            return dbResponse
        } else return searchProject
    } catch (error) {
        throw new InternalHanzoError(StatusCodes.INTERNAL_SERVER_ERROR, `Error: projectServices.createProject - ${getErrorMessage(error)}`)
    }
}

const deleteProject = async (projectId: string): Promise<any> => {
    try {
        const appServer = getRunningExpressApp()
        const dbResponse = await appServer.AppDataSource.getRepository(Projects).delete({ id: projectId })
        return dbResponse
    } catch (error) {
        throw new InternalHanzoError(StatusCodes.INTERNAL_SERVER_ERROR, `Error: projectServices.deleteProject - ${getErrorMessage(error)}`)
    }
}

const getAllProjects = async () => {
    try {
        const appServer = getRunningExpressApp()
        const dbResponse = await appServer.AppDataSource.getRepository(Projects).find()
        return dbResponse
    } catch (error) {
        throw new InternalHanzoError(StatusCodes.INTERNAL_SERVER_ERROR, `Error: projectServices.getAllProjects - ${getErrorMessage(error)}`)
    }
}

const getProjectById = async (projectId: string) => {
    try {
        const appServer = getRunningExpressApp()
        const dbResponse = await appServer.AppDataSource.getRepository(Projects).findOneBy({
            id: projectId
        })
        return dbResponse
    } catch (error) {
        throw new InternalHanzoError(StatusCodes.INTERNAL_SERVER_ERROR, `Error: projectServices.getProjectById - ${getErrorMessage(error)}`)
    }
}

const updateProject = async (project: Projects, updatedProject: Projects) => {
    try {
        const appServer = getRunningExpressApp()
        const tmpUpdatedProject = await appServer.AppDataSource.getRepository(Projects).merge(project, updatedProject)
        const dbResponse = await appServer.AppDataSource.getRepository(Projects).save(tmpUpdatedProject)
        return dbResponse
    } catch (error) {
        throw new InternalHanzoError(StatusCodes.INTERNAL_SERVER_ERROR, `Error: projectServices.updateProject - ${getErrorMessage(error)}`)
    }
}

export default {
    createProject,
    deleteProject,
    getAllProjects,
    getProjectById,
    updateProject
}
