import express from 'express'
import projectsController from '../../controllers/projects'

const router = express.Router()

// CREATE
router.post('/', projectsController.createProject)

// READ
router.get('/', projectsController.getAllProjects)

// UPDATE
router.put(['/', '/:id'], projectsController.updateProject)

// DELETE
router.delete(['/', '/:id'], projectsController.deleteProject)

export default router
