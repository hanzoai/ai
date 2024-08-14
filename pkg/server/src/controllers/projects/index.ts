import { Request, Response, NextFunction } from 'express'
import projectService from '../../services/project'
import { Projects } from '../../database/entities/Projects'
import { InternalHanzoError } from '../../errors/internalHanzoError'
import { StatusCodes } from 'http-status-codes'

const createProject = async (req: Request, res: Response, next: NextFunction) => {
    console.log(req.body)
    try {
        if (typeof req.body === 'undefined') {
            throw new InternalHanzoError(StatusCodes.PRECONDITION_FAILED, `Error: projectsController.createProject - body not provided!`)
        }
        const body = req.body
        const newProject = new Projects()
        Object.assign(newProject, body)
        const apiResponse = await projectService.createProject(newProject)
        return res.json(apiResponse)
    } catch (error) {
        next(error)
    }
}

const deleteProject = async (req: Request, res: Response, next: NextFunction) => {
    try {
        if (typeof req.params === 'undefined' || !req.params.id) {
            throw new InternalHanzoError(StatusCodes.PRECONDITION_FAILED, 'Error: projectsController.deleteProject - id not provided!')
        }
        const apiResponse = await projectService.deleteProject(req.params.id)
        return res.json(apiResponse)
    } catch (error) {
        next(error)
    }
}

const getAllProjects = async (req: Request, res: Response, next: NextFunction) => {
    try {
        const apiResponse = await projectService.getAllProjects()
        return res.json(apiResponse)
    } catch (error) {
        next(error)
    }
}

const updateProject = async (req: Request, res: Response, next: NextFunction) => {
    try {
        if (typeof req.params === 'undefined' || !req.params.id) {
            throw new InternalHanzoError(StatusCodes.PRECONDITION_FAILED, 'Error: projectsController.updateProjects - id not provided!')
        }
        if (typeof req.body === 'undefined') {
            throw new InternalHanzoError(StatusCodes.PRECONDITION_FAILED, 'Error: projectsController.updateProjects - body not provided!')
        }
        const project = await projectService.getProjectById(req.params.id)
        if (!project) {
            return res.status(404).send(`Project ${req.params.id} not found in the database`)
        }
        const body = req.body
        const updatedProject = new Projects()
        Object.assign(updatedProject, body)
        const apiResponse = await projectService.updateProject(project, updatedProject)
        return res.json(apiResponse)
    } catch (error) {
        next(error)
    }
}

export default {
    createProject,
    deleteProject,
    getAllProjects,
    updateProject
}
