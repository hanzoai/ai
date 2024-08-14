import client from './client'

const getAllProjects = () => client.get('/projects')

const createProject = (body) => client.post(`/projects`, body)

const updateProject = (id, body) => client.put(`/projects/${id}`, body)

const deleteProject = (id) => client.delete(`/projects/${id}`)

export default {
    getAllProjects,
    createProject,
    updateProject,
    deleteProject
}
